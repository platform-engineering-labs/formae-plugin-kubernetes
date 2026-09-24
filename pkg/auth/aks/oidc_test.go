//go:build unit

package aks

import (
	"context"
	"errors"
	"fmt"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type identityFunc func(context.Context, string) (string, error)

func (f identityFunc) IdentityToken(c context.Context, a string) (string, error) { return f(c, a) }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(r *http.Request, code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

const tenant = "11111111-1111-4111-8111-111111111111"
const client = "22222222-2222-4222-8222-222222222222"

func TestOidcUsesPublicAzureAndAKSScope(t *testing.T) {
	t.Setenv("AZURE_AUTHORITY_HOST", "https://attacker.invalid/")
	t.Setenv("AZURE_CLIENT_SECRET", "ambient-secret")
	t.Setenv("AZURE_TENANT_ID", "ambient-tenant")
	t.Setenv("AZURE_CLIENT_ID", "ambient-client")
	poisonedFile := t.TempDir() + "/credentials"
	if err := os.WriteFile(poisonedFile, []byte("poisoned ambient credential file; must not be read"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", poisonedFile)
	minted := 0
	exchanges := 0
	src := newOidcTokenSource(identityFunc(func(ctx context.Context, a string) (string, error) {
		minted++
		if a != "api://AzureADTokenExchange" {
			t.Fatalf("audience %s", a)
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		return "broker-secret", nil
	}), tenant, client, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "login.microsoftonline.com" {
			t.Fatalf("unexpected authority %s", r.URL)
		}
		if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			return response(r, 200, fmt.Sprintf(`{"authorization_endpoint":"https://login.microsoftonline.com/%s/oauth2/v2.0/authorize","token_endpoint":"https://login.microsoftonline.com/%s/oauth2/v2.0/token","issuer":"https://login.microsoftonline.com/%s/v2.0"}`, tenant, tenant, tenant)), nil
		}
		if r.Method != "POST" || r.URL.Path != "/"+tenant+"/oauth2/v2.0/token" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
		exchanges++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		for k, v := range map[string]string{"client_id": client, "client_assertion": "broker-secret", "client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer", "grant_type": "client_credentials", "scope": "6dae42f8-4368-4678-94ff-3960e28e3630/.default openid offline_access profile"} {
			if r.Form.Get(k) != v {
				t.Fatalf("%s = %q", k, r.Form.Get(k))
			}
		}
		return response(r, 200, `{"access_token":"aks-token","token_type":"Bearer","expires_in":120}`), nil
	}))
	first, cancel := context.WithCancel(context.Background())
	for i := 0; i < 2; i++ {
		ctx := context.Background()
		if i == 0 {
			ctx = first
		}
		token, exp, err := src.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if token != "aks-token" || time.Until(exp) < 100*time.Second || time.Until(exp) > 121*time.Second {
			t.Fatalf("token %q expiry %s", token, exp)
		}
		cancel()
	}
	if minted != 2 || exchanges != 2 {
		t.Fatalf("minted %d exchanges %d", minted, exchanges)
	}
}

func TestOidcBrokerFailurePreservesIdentity(t *testing.T) {
	sentinel := errors.New("broker-secret")
	src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { return "", sentinel }), tenant, client, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			return response(r, 200, fmt.Sprintf(`{"authorization_endpoint":"https://login.microsoftonline.com/%s/oauth2/v2.0/authorize","token_endpoint":"https://login.microsoftonline.com/%s/oauth2/v2.0/token","issuer":"https://login.microsoftonline.com/%s/v2.0"}`, tenant, tenant, tenant)), nil
		}
		t.Fatalf("exchange after broker failure %s", r.URL)
		return nil, nil
	}))
	if _, _, err := src.Token(context.Background()); !errors.Is(err, sentinel) || strings.Contains(err.Error(), "broker-secret") {
		t.Fatalf("error %v", err)
	}
}

func TestOidcMissingBrokerPreservesSentinel(t *testing.T) {
	src := newOidcTokenSource(nil, tenant, client, roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("network with missing broker"); return nil, nil }))
	_, _, err := src.Token(context.Background())
	if !errors.Is(err, plugin.ErrNoOidcBroker) {
		t.Fatalf("lost missing-broker cause: %v", err)
	}
}

func azureMetadataOnly(t *testing.T) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			return response(r, 200, fmt.Sprintf(`{"authorization_endpoint":"https://login.microsoftonline.com/%s/oauth2/v2.0/authorize","token_endpoint":"https://login.microsoftonline.com/%s/oauth2/v2.0/token","issuer":"https://login.microsoftonline.com/%s/v2.0"}`, tenant, tenant, tenant)), nil
		}
		t.Fatalf("unexpected exchange %s", r.URL)
		return nil, nil
	}
}

func TestOidcRejectsUntrustedMetadataTokenEndpoint(t *testing.T) {
	for _, endpoint := range []string{"https://attacker.invalid/token", "https://login.microsoftonline.com/another-tenant/oauth2/v2.0/token"} {
		calls := 0
		src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { return "broker-secret", nil }), tenant, client, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls > 1 {
				t.Fatal("assertion sent to metadata-controlled endpoint")
			}
			return response(r, 200, fmt.Sprintf(`{"authorization_endpoint":"https://login.microsoftonline.com/%s/oauth2/v2.0/authorize","token_endpoint":%q,"issuer":"https://login.microsoftonline.com/%s/v2.0"}`, tenant, endpoint, tenant)), nil
		}))
		if token, _, err := src.Token(context.Background()); err == nil || token != "" {
			t.Fatal("accepted untrusted endpoint")
		}
	}
}

func TestOidcExchangeFailureNeverLeaksOrFallsBack(t *testing.T) {
	for _, status := range []int{401, 403, 307, 308} {
		for _, location := range []string{"https://attacker.invalid/token", "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token"} {
			t.Run(fmt.Sprint(status)+location, func(t *testing.T) {
				exchanges := 0
				src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { return "broker-secret", nil }), tenant, client, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
						return azureMetadataOnly(t)(r)
					}
					exchanges++
					if exchanges > 1 {
						t.Fatal("redirect/fallback exchange")
					}
					res := response(r, status, `{"error":"invalid_client","error_description":"broker-secret access-secret https://signed.invalid/?secret=value"}`)
					res.Header.Set("Location", location)
					return res, nil
				}))
				token, _, err := src.Token(context.Background())
				if err == nil || token != "" || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "signed.invalid") {
					t.Fatalf("unsafe failure %q %v", token, err)
				}
				if exchanges != 1 {
					t.Fatalf("exchanges %d", exchanges)
				}
			})
		}
	}
}

func TestOidcDeadlineAndNextOperation(t *testing.T) {
	type key struct{}
	calls := 0
	src := newOidcTokenSource(identityFunc(func(ctx context.Context, _ string) (string, error) {
		if ctx.Value(key{}) != "operation" {
			t.Fatal("lost trusted context")
		}
		return "broker-secret", nil
	}), tenant, client, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			return azureMetadataOnly(t)(r)
		}
		calls++
		if calls == 1 {
			if _, ok := r.Context().Deadline(); !ok {
				t.Fatal("missing deadline")
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return response(r, 200, `{"access_token":"fresh-token","token_type":"Bearer","expires_in":3600}`), nil
	}))
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), key{}, "operation"), 30*time.Millisecond)
	defer cancel()
	if _, _, err := src.Token(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline cause %v", err)
	}
	ctx2 := context.WithValue(context.Background(), key{}, "operation")
	if tok, _, err := src.Token(ctx2); err != nil || tok != "fresh-token" {
		t.Fatalf("next operation %q %v", tok, err)
	}
}

func TestOidcCanceledCallerCannotMint(t *testing.T) {
	broker := identityFunc(func(context.Context, string) (string, error) {
		t.Fatal("minted for canceled operation")
		return "", nil
	})
	src := NewOidcTokenSource(broker, tenant, client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if token, _, err := src.Token(ctx); token != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled result %q %v", token, err)
	}
}

func TestOidcConcurrentBrokerErrorsRemainOperationLocal(t *testing.T) {
	type errorKey struct{}
	src := newOidcTokenSource(identityFunc(func(ctx context.Context, _ string) (string, error) { return "", ctx.Value(errorKey{}).(error) }), tenant, client, roundTripFunc(azureMetadataOnly(t)))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cause := errors.New("private-broker-error")
			ctx := context.WithValue(context.Background(), errorKey{}, cause)
			_, _, err := src.Token(ctx)
			if !errors.Is(err, cause) || strings.Contains(err.Error(), "private-broker-error") {
				t.Errorf("lost operation-local cause %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestOidcRejectsEmptyOrNearExpiredAzureToken(t *testing.T) {
	for _, body := range []string{`{"access_token":"","token_type":"Bearer","expires_in":3600}`, `{"access_token":"token","token_type":"Bearer","expires_in":5}`} {
		src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { return "broker-secret", nil }), tenant, client, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
				return azureMetadataOnly(t)(r)
			}
			return response(r, 200, body), nil
		}))
		if token, _, err := src.Token(context.Background()); err == nil || token != "" {
			t.Fatal("accepted unsafe token")
		}
	}
}
