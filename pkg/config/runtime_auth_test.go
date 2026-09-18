//go:build unit

package config_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

type runtimeBroker func(context.Context, string) (string, error)

func (f runtimeBroker) IdentityToken(ctx context.Context, audience string) (string, error) {
	return f(ctx, audience)
}

type runtimeTransport func(*http.Request) (*http.Response, error)

func (f runtimeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func runtimeJWT() string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))) + ".eA"
}

func TestRuntimeExplicitSelectsSourceWithoutAmbientFallback(t *testing.T) {
	for _, typ := range []string{"EKS", "AKS", "GKE", "Oidc"} {
		t.Run(typ, func(t *testing.T) {
			cfg := parseExplicit(t, typ)
			_, err := cfg.ToK8sConfig(context.Background())
			if !errors.Is(err, plugin.ErrNoOidcBroker) {
				t.Fatalf("missing source must fail as missing broker, got %v", err)
			}
			cfg.SetAuthDependencies(config.AuthDependencies{OidcSource: runtimeBroker(func(context.Context, string) (string, error) { t.Fatal("constructor minted token"); return "", nil })})
			if _, err := cfg.ToK8sConfig(context.Background()); err != nil {
				t.Fatalf("validated explicit provider should construct without ambient access: %v", err)
			}
		})
	}
}

func TestRuntimeDirectUsesLiveOperationAndRejectsBackground(t *testing.T) {
	cfg := parseExplicit(t, "Oidc")
	calls := 0
	cfg.SetAuthDependencies(config.AuthDependencies{OidcSource: runtimeBroker(func(ctx context.Context, aud string) (string, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			t.Error("credential servicing lacks 30s bound")
		}
		return runtimeJWT(), nil
	})})
	rc, err := cfg.ToK8sConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tr := rc.WrapTransport(runtimeTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("missing bearer")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	}))
	req, _ := http.NewRequest("GET", rc.Host, nil)
	if _, err := tr.RoundTrip(req); !errors.Is(err, plugin.ErrNoOidcBroker) {
		t.Fatalf("background must fail before mint: %v", err)
	}
	if calls != 0 {
		t.Fatal("background minted")
	}
	op, cancel := config.NewOperationContext(context.Background())
	response, err := tr.RoundTrip(req.WithContext(op))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if calls != 1 {
		t.Fatalf("expected one mint, got %d", calls)
	}
	cancel()
	if _, err := tr.RoundTrip(req.WithContext(context.WithoutCancel(op))); !errors.Is(err, context.Canceled) {
		t.Fatalf("completed operation used cached bearer: %v", err)
	}
	next, done := config.NewOperationContext(context.Background())
	defer done()
	response, err = tr.RoundTrip(req.WithContext(next))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if calls != 1 {
		t.Fatal("same client lost its token cache")
	}

}
