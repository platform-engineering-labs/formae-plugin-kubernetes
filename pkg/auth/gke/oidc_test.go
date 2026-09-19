//go:build unit

package gke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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

const testProvider = "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/pool/providers/provider"

func TestOidcExplicitFederationAndImpersonation(t *testing.T) {
	poisonedFile := t.TempDir() + "/credentials"
	if err := os.WriteFile(poisonedFile, []byte("poisoned ambient credential file; must not be read"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", poisonedFile)
	t.Setenv("GCE_METADATA_HOST", "attacker.invalid")
	for _, sa := range []string{"", "worker@project.iam.gserviceaccount.com"} {
		t.Run(sa, func(t *testing.T) {
			calls := 0
			minted := 0
			exp := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
			src := newOidcTokenSource(identityFunc(func(ctx context.Context, a string) (string, error) {
				minted++
				if a != testProvider {
					t.Fatalf("audience %q", a)
				}
				return "broker-secret", nil
			}), testProvider, sa, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" {
					t.Fatal(r.Method)
				}
				switch r.URL.Host {
				case "sts.googleapis.com":
					if r.URL.Path != "/v1/token" || r.Header.Get("Authorization") != "" {
						t.Fatalf("unexpected request %s", r.URL)
					}
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					for k, v := range map[string]string{"audience": testProvider, "grant_type": "urn:ietf:params:oauth:grant-type:token-exchange", "requested_token_type": "urn:ietf:params:oauth:token-type:access_token", "subject_token_type": "urn:ietf:params:oauth:token-type:jwt", "subject_token": "broker-secret", "scope": "https://www.googleapis.com/auth/cloud-platform"} {
						if r.Form.Get(k) != v {
							t.Fatalf("%s = %q", k, r.Form.Get(k))
						}
					}
					return response(r, 200, `{"access_token":"federated-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":1800}`), nil
				case "iamcredentials.googleapis.com":
					if sa == "" || r.URL.Path != "/v1/projects/-/serviceAccounts/worker@project.iam.gserviceaccount.com:generateAccessToken" || r.Header.Get("Authorization") != "Bearer federated-token" {
						t.Fatalf("incorrect IAM request %s", r.URL)
					}
					var body struct {
						Scope []string `json:"scope"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if strings.Join(body.Scope, " ") != "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email" {
						t.Fatalf("scopes %v", body.Scope)
					}
					return response(r, 200, `{"accessToken":"impersonated-token","expireTime":"`+exp.Format(time.RFC3339)+`"}`), nil
				default:
					t.Fatalf("ambient or unexpected probe %s", r.URL)
					return nil, nil
				}
			}))
			first, cancel := context.WithCancel(context.Background())
			for i := 0; i < 2; i++ {
				ctx := context.Background()
				if i == 0 {
					ctx = first
				}
				token, expiry, err := src.Token(ctx)
				if err != nil {
					t.Fatal(err)
				}
				want := "federated-token"
				if sa != "" {
					want = "impersonated-token"
					if !expiry.Equal(exp) {
						t.Fatalf("impersonated expiry %s want %s", expiry, exp)
					}
				}
				if token != want || time.Until(expiry) < 29*time.Minute || time.Until(expiry) > 31*time.Minute {
					t.Fatalf("token %q expiry %s", token, expiry)
				}
				cancel()
			}
			wantCalls := 2
			if sa != "" {
				wantCalls = 4
			}
			if calls != wantCalls || minted != 2 {
				t.Fatalf("calls %d minted %d", calls, minted)
			}
		})
	}
}

func TestOidcBrokerErrorPreventsExchange(t *testing.T) {
	sentinel := errors.New("broker-secret")
	src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { return "", sentinel }), testProvider, "", roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("network after broker error"); return nil, nil }))
	if _, _, err := src.Token(context.Background()); !errors.Is(err, sentinel) || strings.Contains(err.Error(), "broker-secret") {
		t.Fatalf("error %v", err)
	}
}

func TestOidcExchangeFailureNeverLeaksOrFallsBack(t *testing.T) {
	for _, stage := range []string{"sts", "iam"} {
		for _, status := range []int{401, 403, 307, 308} {
			for _, location := range []string{"https://attacker.invalid/token", "https://sts.googleapis.com/v1/token"} {
				t.Run(fmt.Sprint(status)+stage+location, func(t *testing.T) {
					calls := 0
					wantCalls := 1
					sa := ""
					if stage == "iam" {
						sa = "worker@project.iam.gserviceaccount.com"
						wantCalls = 2
					}
					src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { return "broker-secret", nil }), testProvider, sa, roundTripFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						if calls > wantCalls {
							t.Fatal("redirect/fallback exchange")
						}
						if stage == "iam" && calls == 1 {
							return response(r, 200, `{"access_token":"federated-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":1800}`), nil
						}
						res := response(r, status, `{"error":"invalid_request","error_description":"broker-secret access-secret https://signed.invalid/?secret=value"}`)
						res.Header.Set("Location", location)
						return res, nil
					}))
					token, _, err := src.Token(context.Background())
					if err == nil || token != "" || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "signed.invalid") {
						t.Fatalf("unsafe failure %q %v", token, err)
					}
					if calls != wantCalls {
						t.Fatalf("calls %d", calls)
					}
				})
			}
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
	}), testProvider, "", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if _, ok := r.Context().Deadline(); !ok {
				t.Fatal("missing deadline")
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return response(r, 200, `{"access_token":"fresh-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":1800}`), nil
	}))
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), key{}, "operation"), 30*time.Millisecond)
	defer cancel()
	if _, _, err := src.Token(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline cause %v", err)
	}
	if tok, _, err := src.Token(context.WithValue(context.Background(), key{}, "operation")); err != nil || tok != "fresh-token" {
		t.Fatalf("next operation %q %v", tok, err)
	}
}

func TestOidcRejectsEmptyOrNearExpiredAccessToken(t *testing.T) {
	for _, body := range []string{`{"access_token":"","expires_in":3600}`, `{"access_token":"token","expires_in":5}`, `{"access_token":"token","expires_in":0}`} {
		src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { return "broker-secret", nil }), testProvider, "", roundTripFunc(func(r *http.Request) (*http.Response, error) { return response(r, 200, body), nil }))
		if tok, _, err := src.Token(context.Background()); err == nil || tok != "" {
			t.Fatal("accepted unsafe token")
		}
	}
}

func TestOidcCanceledCallerCannotMint(t *testing.T) {
	broker := identityFunc(func(context.Context, string) (string, error) {
		t.Fatal("minted for canceled operation")
		return "", nil
	})
	src := NewOidcTokenSource(broker, testProvider, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if token, _, err := src.Token(ctx); token != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled result %q %v", token, err)
	}
}
