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
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/client-go/rest"
)

type helmOperationKey struct{}
type helmTestBroker func(context.Context, string) (string, error)

func (f helmTestBroker) IdentityToken(c context.Context, a string) (string, error) { return f(c, a) }
func helmJWT(id string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":4102444800,"id":%q}`, id))) + ".eA"
}
func helmOidcConfig(t *testing.T, s *httptest.Server) *config.Config {
	t.Helper()
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}))
	raw := fmt.Sprintf(`{"Auth":{"Type":"Oidc","Endpoint":%q,"CertificateAuthority":%q,"Audience":"urn:formae:kubernetes:39c24d1d-3815-4817-9242-4032be46601b"}}`, s.URL, ca)
	cfg, err := config.FromTargetConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetAuthDependencies(config.AuthDependencies{OidcSource: helmTestBroker(func(ctx context.Context, _ string) (string, error) {
		id, _ := ctx.Value(helmOperationKey{}).(string)
		if id == "" {
			return "", plugin.ErrNoOidcBroker
		}
		return helmJWT(id), nil
	})})
	return cfg
}

func TestSynchronousHelmStorageUsesCallOwnedContext(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		want := "A"
		if strings.Contains(r.URL.Path, "ns-B") {
			want = "B"
		}
		if r.Header.Get("Authorization") != "Bearer "+helmJWT(want) {
			t.Error("missing callback bearer")
		}
		fmt.Fprint(w, `{"kind":"SecretList","apiVersion":"v1","metadata":{},"items":[]}`)
	}))
	defer server.Close()
	cfg := helmOidcConfig(t, server)
	for _, name := range []string{"A", "B"} {
		ctx, cancel := config.NewOperationContext(context.WithValue(context.Background(), helmOperationKey{}, name))
		conf, err := newActionConfig(ctx, cfg, "ns-"+name)
		if err != nil {
			t.Fatal(err)
		}
		_, err = conf.Releases.List(func(*release.Release) bool { return true })
		if err != nil {
			t.Fatalf("storage %s: %v", name, err)
		}
		cancel()
		if _, err = conf.Releases.List(func(*release.Release) bool { return true }); err == nil {
			t.Fatal("finished callback reused storage adapter")
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("expected exactly two live storage reads, got %d", hits.Load())
	}
}

func TestInventoryAuthKeysAndMissingBroker(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	cfg := helmOidcConfig(t, server)
	ctx, cancel := config.NewOperationContext(context.Background())
	defer cancel()
	meta := func(id string) func(context.Context) (plugin.OidcOperationInfo, bool) {
		return func(context.Context) (plugin.OidcOperationInfo, bool) {
			return plugin.OidcOperationInfo{BindingID: id}, id != ""
		}
	}
	a, ok, err := inventoryCacheKey(ctx, cfg, meta("A"))
	if err != nil || !ok {
		t.Fatal(err)
	}
	b, ok, err := inventoryCacheKey(ctx, cfg, meta("B"))
	if err != nil || !ok || a == b {
		t.Fatal("bindings shared authorization-filtered inventory")
	}
	if _, ok, err := inventoryCacheKey(ctx, cfg, meta("")); err != nil || ok {
		t.Fatal("missing metadata reused inventory")
	}
	cfg.SetAuthDependencies(config.AuthDependencies{OidcSource: plugin.NewOidcTokenSource()})
	for _, ns := range []string{"", "first", "second"} {
		conf, err := newActionConfig(ctx, cfg, ns)
		if err != nil {
			t.Fatal(err)
		}
		_, err = conf.Releases.List(func(*release.Release) bool { return true })
		if !errors.Is(err, plugin.ErrNoOidcBroker) {
			t.Fatalf("namespace %q: %v", ns, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("missing broker reached storage API")
	}
}

func TestWorkerConfigUsesOnlyQueueTokens(t *testing.T) {
	var seen string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"kind":"SecretList","apiVersion":"v1","metadata":{},"items":[]}`)
	}))
	defer server.Close()
	cfg := helmOidcConfig(t, server)
	ctx, cancel := config.NewOperationContext(context.WithValue(context.Background(), helmOperationKey{}, "live"))
	bridge, _ := testBridge(t)
	rc, source, err := cfg.OidcWorkerConfig(ctx, bridge)
	if err != nil {
		t.Fatal(err)
	}
	if err = bridge.Service(ctx, source); err != nil {
		t.Fatal(err)
	}
	cancel()
	client, err := rest.HTTPClientFor(rc)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(server.URL + "/api/v1/namespaces/ns/secrets")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen != "Bearer "+helmJWT("live") {
		t.Fatal("worker did not use prefetched data")
	}
	if _, _, err = source.Token(ctx); err == nil {
		t.Fatal("ended callback minted")
	}
	bridge.Close(context.Canceled)
	if _, err = client.Get(server.URL); err == nil {
		t.Fatal("closed worker sent credentials")
	}
}
