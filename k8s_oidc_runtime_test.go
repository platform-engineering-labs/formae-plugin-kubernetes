//go:build unit

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

type pluginTestBroker func(context.Context, string) (string, error)

func (f pluginTestBroker) IdentityToken(ctx context.Context, a string) (string, error) {
	return f(ctx, a)
}
func pluginJWT(id string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"id":%q}`, int64(4102444800), id))) + ".eA"
}
func pluginTarget(t *testing.T, s *httptest.Server) []byte {
	t.Helper()
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}))
	return []byte(fmt.Sprintf(`{"Auth":{"Type":"Oidc","Endpoint":%q,"CertificateAuthority":%q,"Audience":"urn:formae:kubernetes:39c24d1d-3815-4817-9242-4032be46601b"}}`, s.URL, ca))
}

func TestPluginInstancesUseTheirOwnOidcSource(t *testing.T) {
	var headers []string
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		headers = append(headers, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path == "/version" {
			fmt.Fprint(w, `{"major":"1","minor":"36","gitVersion":"v1.36.0"}`)
			return
		}
		fmt.Fprint(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"demo","uid":"example","resourceVersion":"1"},"status":{"phase":"Active"}}`)
	}))
	defer server.Close()
	target := pluginTarget(t, server)
	plugins := map[string]*Plugin{}
	for _, id := range []string{"first", "second"} {
		p := &Plugin{}
		p.SetOidcTokenSource(pluginTestBroker(func(ctx context.Context, a string) (string, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("credential service lacks deadline")
			}
			return pluginJWT(id), nil
		}))
		plugins[id] = p
	}
	for _, id := range []string{"first", "second", "first"} {
		p := plugins[id]
		mu.Lock()
		start := len(headers)
		mu.Unlock()
		result, err := p.Read(context.Background(), &resource.ReadRequest{ResourceType: "K8S::Core::Namespace", NativeID: "demo", TargetConfig: target})
		if err != nil || result == nil {
			t.Fatalf("read: %v", err)
		}
		mu.Lock()
		received := append([]string(nil), headers[start:]...)
		mu.Unlock()
		if len(received) == 0 {
			t.Fatal("read performed no request")
		}
		for _, header := range received {
			if header != "Bearer "+pluginJWT(id) {
				t.Fatalf("instance %s used another identity", id)
			}
		}
	}
}

func TestMissingBrokerFailsEveryCallbackWithoutNetwork(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	target := pluginTarget(t, server)
	for _, installed := range []bool{false, true} {
		p := &Plugin{}
		if installed {
			p.SetOidcTokenSource(plugin.NewOidcTokenSource())
		}
		props := json.RawMessage(`{"metadata":{"name":"demo"}}`)
		calls := map[string]func() error{
			"create": func() error {
				_, e := p.Create(context.Background(), &resource.CreateRequest{ResourceType: "K8S::Core::Namespace", TargetConfig: target, Properties: props})
				return e
			},
			"read": func() error {
				_, e := p.Read(context.Background(), &resource.ReadRequest{ResourceType: "K8S::Core::Namespace", TargetConfig: target, NativeID: "demo"})
				return e
			},
			"update": func() error {
				_, e := p.Update(context.Background(), &resource.UpdateRequest{ResourceType: "K8S::Core::Namespace", TargetConfig: target, NativeID: "demo", DesiredProperties: props})
				return e
			},
			"delete": func() error {
				_, e := p.Delete(context.Background(), &resource.DeleteRequest{ResourceType: "K8S::Core::Namespace", TargetConfig: target, NativeID: "demo"})
				return e
			},
			"status": func() error {
				_, e := p.Status(context.Background(), &resource.StatusRequest{ResourceType: "K8S::Core::Namespace", TargetConfig: target, NativeID: "demo"})
				return e
			},
			"list": func() error {
				_, e := p.List(context.Background(), &resource.ListRequest{ResourceType: "K8S::Core::Namespace", TargetConfig: target})
				return e
			},
		}
		for name, call := range calls {
			t.Run(fmt.Sprintf("installed=%v/%s", installed, name), func(t *testing.T) {
				if err := call(); !errors.Is(err, plugin.ErrNoOidcBroker) {
					t.Fatalf("missing broker: %v", err)
				}
			})
		}
	}
	if hits.Load() != 0 {
		t.Fatal("missing broker reached API")
	}
}

func TestPolicyAndHelmMetadataPrecedeVersionDiscovery(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	target := pluginTarget(t, server)
	p := &Plugin{}
	p.SetOidcTokenSource(pluginTestBroker(func(context.Context, string) (string, error) {
		t.Error("unauthorized callback minted credentials")
		return pluginJWT("bad"), nil
	}))
	if err := p.Configure([]byte(`{"allowedAuthMethods":["Kubeconfig"]}`)); err != nil {
		t.Fatal(err)
	}
	_, err := p.Read(context.Background(), &resource.ReadRequest{ResourceType: "K8S::Core::Namespace", TargetConfig: target, NativeID: "demo"})
	if err == nil || !strings.Contains(err.Error(), "operator policy") {
		t.Fatalf("policy: %v", err)
	}
	if err := p.Configure(nil); err != nil {
		t.Fatal(err)
	}
	_, err = p.Delete(context.Background(), &resource.DeleteRequest{ResourceType: "K8S::Helm::Release", TargetConfig: target, NativeID: "default/demo"})
	if err == nil || !strings.Contains(err.Error(), "trusted operation metadata") {
		t.Fatalf("metadata: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("unauthorized callback reached version discovery")
	}
}
