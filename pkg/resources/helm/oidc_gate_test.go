//go:build unit

package helm

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestBrokerReleaseRequiresAuthorityBeforeSideEffects(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	raw := fmt.Sprintf(`{"Auth":{"Type":"Oidc","Endpoint":%q,"CertificateAuthority":%q,"Audience":"urn:formae:kubernetes:39c24d1d-3815-4817-9242-4032be46601b"}}`, server.URL, ca)
	cfg, err := config.FromTargetConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r := &Release{Config: cfg}
	properties := []byte(`{"metadata":{"name":"demo","namespace":"default"},"chart":"unused-before-authority","timeoutSeconds":300}`)
	calls := map[string]func() error{
		"create": func() error {
			_, e := r.Create(context.Background(), &resource.CreateRequest{Properties: properties})
			return e
		},
		"update": func() error {
			_, e := r.Update(context.Background(), &resource.UpdateRequest{DesiredProperties: properties})
			return e
		},
		"read": func() error {
			_, e := r.Read(context.Background(), &resource.ReadRequest{NativeID: "default/demo"})
			return e
		},
		"delete": func() error {
			_, e := r.Delete(context.Background(), &resource.DeleteRequest{NativeID: "default/demo"})
			return e
		},
		"status": func() error {
			_, e := r.Status(context.Background(), &resource.StatusRequest{RequestID: requestID("default", "demo", 1, opInstall)})
			return e
		},
		"list": func() error { _, e := r.List(context.Background(), &resource.ListRequest{}); return e },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if name == "read" || name == "list" {
				if !errors.Is(err, plugin.ErrNoOidcBroker) {
					t.Fatalf("missing credential source: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "trusted operation metadata") {
				t.Fatalf("missing trusted metadata: %v", err)
			}
		})
	}
	if _, err := newAsyncActionConfig(context.Background(), cfg, "default"); err == nil || !strings.Contains(err.Error(), "OIDC Helm workers require the credential bridge") {
		t.Fatalf("legacy worker constructor must reject broker authentication: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("unauthorized release touched network")
	}
}

func TestInventoryInvalidationSpansAuthIdentities(t *testing.T) {
	first, err := config.FromTargetConfig([]byte(`{"Auth":{"Type":"EKS","Endpoint":"https://same.example","CertificateAuthority":"Y2E=","ClusterName":"prod","Region":"us-east-1","Profile":"one"}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := config.FromTargetConfig([]byte(`{"Auth":{"Type":"EKS","Endpoint":"https://same.example","CertificateAuthority":"Y2E=","ClusterName":"prod","Region":"us-east-1","Profile":"two"}}`))
	if err != nil {
		t.Fatal(err)
	}
	key1, _ := first.AuthFingerprint()
	key2, _ := second.AuthFingerprint()
	invMu.Lock()
	previous := invCache
	invCache = map[string]inventoryEntry{
		key1:        {inv: newInventory(), clusterKey: "Endpoint|https://same.example"},
		key2:        {inv: newInventory(), clusterKey: "Endpoint|https://same.example"},
		"unrelated": {inv: newInventory(), clusterKey: "Endpoint|https://other.example"},
	}
	invMu.Unlock()
	defer func() { invMu.Lock(); invCache = previous; invMu.Unlock() }()
	invalidateInventory(first)
	invMu.Lock()
	remaining := len(invCache)
	invMu.Unlock()
	if remaining != 0 {
		t.Fatalf("conservative alias-safe invalidation retained entries: %d", remaining)
	}
}
