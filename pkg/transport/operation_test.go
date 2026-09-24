//go:build unit

package transport

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

type operationValue struct{}
type testBroker func(context.Context, string) (string, error)

func (f testBroker) IdentityToken(c context.Context, a string) (string, error) { return f(c, a) }
func jwtFor(op string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"op":%q}`, int64(4102444800), op))) + ".eA"
}
func oidcConfig(t *testing.T, s *httptest.Server) *config.Config {
	t.Helper()
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}))
	raw := fmt.Sprintf(`{"Auth":{"Type":"Oidc","Endpoint":%q,"CertificateAuthority":%q,"Audience":"urn:formae:kubernetes:39c24d1d-3815-4817-9242-4032be46601b"}}`, s.URL, ca)
	cfg := mustConfig(t, raw)
	cfg.SetAuthDependencies(config.AuthDependencies{OidcSource: testBroker(func(ctx context.Context, _ string) (string, error) {
		op, _ := ctx.Value(operationValue{}).(string)
		if op == "" {
			return "", plugin.ErrNoOidcBroker
		}
		return jwtFor(op), nil
	})})
	return cfg
}
func operation(name string) (context.Context, context.CancelFunc) {
	return config.NewOperationContext(context.WithValue(context.Background(), operationValue{}, name))
}

func TestDiscoveryUsesCurrentOperationAfterPreviousCallbackEnds(t *testing.T) {
	var phase atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if phase.Load() == 0 {
			http.Error(w, "temporarily unavailable", 503)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+jwtFor("B") {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"major": "1", "minor": "36", "gitVersion": "v1.36.0"})
	}))
	defer server.Close()
	cfg := oidcConfig(t, server)
	a, done := operation("A")
	client, err := NewClient(a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResolveVersion(a); err == nil {
		t.Fatal("first discovery should fail")
	}
	done()
	phase.Store(1)
	b, cancel := operation("B")
	defer cancel()
	got, err := client.ResolveVersion(b)
	if err != nil || got != "1.36" {
		t.Fatalf("fresh callback discovery = %q, %v", got, err)
	}
}

func TestClientCacheBindingAndMissingMetadata(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	cfg := oidcConfig(t, server)
	build := &stubBuilder{}
	meta := func(ctx context.Context) (plugin.OidcOperationInfo, bool) {
		v, _ := ctx.Value(operationValue{}).(string)
		return plugin.OidcOperationInfo{BindingID: v}, v != ""
	}
	cache := newClientCache(build.build, meta)
	a, ca := operation("A")
	defer ca()
	b, cb := operation("B")
	defer cb()
	a1, err := cache.Get(a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := cache.Get(a, cfg)
	b1, _ := cache.Get(b, cfg)
	if a1 != a2 || a1 == b1 {
		t.Fatal("binding must select independent reusable client/token caches")
	}
	old, cancel := config.NewOperationContext(context.Background())
	defer cancel()
	first, _ := cache.Get(old, cfg)
	second, _ := cache.Get(old, cfg)
	if first == second {
		t.Fatal("missing metadata reused client")
	}
	other := newClientCache(build.build, meta)
	third, _ := other.Get(a, cfg)
	if third == a1 {
		t.Fatal("separate instance shared client")
	}
	legacy := mustConfig(t, `{"Auth":{"Type":"Kubeconfig","Context":"test"}}`)
	la, _ := cache.Get(a, legacy)
	lb, _ := cache.Get(b, legacy)
	lc, _ := cache.Get(context.Background(), legacy)
	if la != lb || la != lc {
		t.Fatal("legacy cache depends on broker metadata")
	}
}

func TestMapperRefreshUsesNewOperationAndCachesOnlyData(t *testing.T) {
	var phase atomic.Int32
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if phase.Load() > 0 && r.Header.Get("Authorization") != "Bearer "+jwtFor("B") {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api":
			fmt.Fprint(w, `{"kind":"APIVersions","apiVersion":"v1","versions":["v1"]}`)
		case "/apis":
			fmt.Fprint(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"example.io","versions":[{"groupVersion":"example.io/v1","version":"v1"}],"preferredVersion":{"groupVersion":"example.io/v1","version":"v1"}}]}`)
		case "/api/v1":
			fmt.Fprint(w, `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"v1","resources":[{"name":"namespaces","kind":"Namespace","namespaced":false,"verbs":["get","list"]}]}`)
		case "/apis/example.io/v1":
			resources := `{"name":"widgets","kind":"Widget","namespaced":true,"verbs":["get","list"]}`
			if phase.Load() > 0 {
				resources += `,{"name":"gadgets","kind":"Gadget","namespaced":true,"verbs":["get","list"]}`
			}
			fmt.Fprintf(w, `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"example.io/v1","resources":[%s]}`, resources)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := oidcConfig(t, server)
	a, done := operation("A")
	client, err := NewClient(a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	gvr, ns, err := client.ResolveMapping(a, "example.io/v1", "Widget")
	if err != nil || gvr.Resource != "widgets" || !ns {
		t.Fatalf("first mapping: %v %v", gvr, err)
	}
	before := hits.Load()
	_, _, err = client.ResolveMapping(a, "example.io/v1", "Widget")
	if err != nil || hits.Load() != before {
		t.Fatal("successful mapping did not reuse data")
	}
	done()
	phase.Store(1)
	b, cancel := operation("B")
	defer cancel()
	gvr, ns, err = client.ResolveMapping(b, "example.io/v1", "Gadget")
	if err != nil || gvr.Resource != "gadgets" || !ns {
		t.Fatalf("reset-on-miss mapping: %v %v", gvr, err)
	}
	gvr, ns, ok := client.ResolveKind(b, "Gadget")
	if !ok || !ns || gvr.Resource != "gadgets" {
		t.Fatalf("preferred discovery: %v %v", gvr, ok)
	}
	if _, _, err := client.ResolveMapping(context.Background(), "example.io/v1", "Widget"); err == nil {
		t.Fatal("cached mapping accepted a raw background caller")
	}
	if _, _, ok := client.ResolveKind(context.Background(), "Gadget"); ok {
		t.Fatal("cached kind accepted a raw background caller")
	}
}

func TestSharedCallbackCredentialsRemainBindingAndIdentityScoped(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	cfg := oidcConfig(t, server)
	cfg.SetAuthDependencies(config.AuthDependencies{OidcSource: testBroker(func(ctx context.Context, _ string) (string, error) {
		calls.Add(1)
		return jwtFor(ctx.Value(operationValue{}).(string)), nil
	})})
	meta := func(ctx context.Context) (plugin.OidcOperationInfo, bool) {
		id, _ := ctx.Value(operationValue{}).(string)
		return plugin.OidcOperationInfo{BindingID: id}, id != ""
	}
	cache := newClientCache(NewClient, meta)
	for _, binding := range []string{"A", "A", "B", "B"} {
		ctx, cancel := operation(binding)
		client, err := cache.Get(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, source, err := client.OidcWorkerConfig(ctx, unusedWorkerSource{})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := source.Token(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
		if _, _, err := source.Token(ctx); err == nil {
			t.Fatal("closed callback read shared token")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("repeated callbacks mint count %d, want two isolated bindings", calls.Load())
	}
	ctx, cancel := operation("A")
	defer cancel()
	other := oidcConfig(t, server)
	other.SetAuthDependencies(config.AuthDependencies{OidcSource: testBroker(func(context.Context, string) (string, error) { calls.Add(1); return jwtFor("other"), nil })})
	// A public audience change must not reuse the first identity's token cache.
	raw := string(other.Auth)
	other.Auth = []byte(strings.Replace(raw, "39c24d1d-3815-4817-9242-4032be46601b", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1))
	// Parse again so immutable validated authRaw follows the new identity.
	data, _ := json.Marshal(other)
	other = mustConfig(t, string(data))
	other.SetAuthDependencies(config.AuthDependencies{OidcSource: testBroker(func(context.Context, string) (string, error) { calls.Add(1); return jwtFor("other"), nil })})
	client, err := cache.Get(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.oidcTokens.Token(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal("audience identity shared credentials")
	}
}

type unusedWorkerSource struct{}

func (unusedWorkerSource) Token(context.Context) (string, time.Time, error) {
	return "", time.Time{}, errors.New("worker source must not be called by handler")
}
