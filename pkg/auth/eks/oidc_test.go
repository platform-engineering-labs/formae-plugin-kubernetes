//go:build unit

package eks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

func TestOidcExchangeSignsExplicitCredentialsAndBoundsExpiry(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AWS_ENDPOINT_URL_STS", "https://attacker.invalid")
	poisonedFile := t.TempDir() + "/credentials"
	if err := os.WriteFile(poisonedFile, []byte("poisoned ambient credential file; must not be read"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", poisonedFile)
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/missing")
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for _, ttl := range []time.Duration{5 * time.Minute, time.Hour} {
		t.Run(ttl.String(), func(t *testing.T) {
			calls := 0
			src := newOidcTokenSource(identityFunc(func(ctx context.Context, a string) (string, error) {
				if a != "sts.amazonaws.com" {
					t.Fatalf("audience %q", a)
				}
				return "broker-secret", nil
			}), "arn:aws:iam::123456789012:role/test", "us-east-1", "cluster", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://sts.us-east-1.amazonaws.com/" || r.Method != "POST" || r.Header.Get("Authorization") != "" {
					t.Fatalf("unexpected signed exchange %s %s", r.Method, r.URL)
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				for k, v := range map[string]string{"Action": "AssumeRoleWithWebIdentity", "Version": "2011-06-15", "RoleArn": "arn:aws:iam::123456789012:role/test", "RoleSessionName": "formae-kubernetes", "WebIdentityToken": "broker-secret"} {
					if r.Form.Get(k) != v {
						t.Fatalf("%s = %q", k, r.Form.Get(k))
					}
				}
				return response(r, 200, fmt.Sprintf(`<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>explicit-key</AccessKeyId><SecretAccessKey>explicit-secret</SecretAccessKey><SessionToken>session-secret</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`, now.Add(ttl).Format(time.RFC3339))), nil
			}), func() time.Time { return now })
			token, exp, err := src.Token(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls %d", calls)
			}
			raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "k8s-aws-v1."))
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			if u.Host != "sts.us-east-1.amazonaws.com" || q.Get("X-Amz-SignedHeaders") != "host;x-k8s-aws-id" || q.Get("X-Amz-Security-Token") != "session-secret" || !strings.HasPrefix(q.Get("X-Amz-Credential"), "explicit-key/") || q.Get("X-Amz-Expires") != "60" {
				t.Fatalf("incorrect presign query %v", q)
			}
			want := now.Add(14 * time.Minute)
			if ttl == 5*time.Minute {
				want = now.Add(4 * time.Minute)
			}
			if !exp.Equal(want) {
				t.Fatalf("expiry %s want %s", exp, want)
			}
		})
	}
}

func TestOidcFailsClosed(t *testing.T) {
	sentinel := errors.New("broker-secret")
	for _, region := range []string{"cn-north-1", "us-gov-west-1", "us-iso-east-1", "us-east-1.attacker.invalid"} {
		src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { t.Fatal("mint for unsupported region"); return "", nil }), "arn:aws:iam::123456789012:role/test", region, "cluster", roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("network"); return nil, nil }), time.Now)
		if _, _, err := src.Token(context.Background()); err == nil {
			t.Errorf("accepted %s", region)
		}
	}
	src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { return "", sentinel }), "arn:aws:iam::123456789012:role/test", "us-east-1", "cluster", roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("network after broker error"); return nil, nil }), time.Now)
	if _, _, err := src.Token(context.Background()); !errors.Is(err, sentinel) || strings.Contains(err.Error(), "broker-secret") {
		t.Fatalf("error %v", err)
	}
}

func TestOidcExchangeFailureNeverLeaksOrFallsBack(t *testing.T) {
	for _, status := range []int{401, 403, 307, 308} {
		for _, location := range []string{"https://attacker.invalid/", "https://sts.us-east-1.amazonaws.com/"} {
			t.Run(fmt.Sprint(status)+location, func(t *testing.T) {
				calls := 0
				src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { return "broker-secret", nil }), "arn:aws:iam::123456789012:role/test", "us-east-1", "cluster", roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if calls > 1 {
						t.Fatal("redirect/fallback exchange")
					}
					res := response(r, status, `<ErrorResponse><Error><Code>InvalidIdentityToken</Code><Message>broker-secret access-secret https://signed.invalid/?secret=value</Message></Error></ErrorResponse>`)
					res.Header.Set("Location", location)
					return res, nil
				}), time.Now)
				token, _, err := src.Token(context.Background())
				if err == nil || token != "" || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "signed.invalid") {
					t.Fatalf("unsafe failure %q %v", token, err)
				}
				if calls != 1 {
					t.Fatalf("calls %d", calls)
				}
			})
		}
	}
}

func TestOidcRejectsIncompleteOrNearExpiredCredentials(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for _, credential := range []string{"", `<AccessKeyId>key</AccessKeyId>`, fmt.Sprintf(`<AccessKeyId>key</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken><Expiration>%s</Expiration>`, now.Add(70*time.Second).Format(time.RFC3339))} {
		src := newOidcTokenSource(identityFunc(func(context.Context, string) (string, error) { return "broker-secret", nil }), "arn:aws:iam::123456789012:role/test", "us-east-1", "cluster", roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return response(r, 200, `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials>`+credential+`</Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`), nil
		}), func() time.Time { return now })
		if token, _, err := src.Token(context.Background()); err == nil || token != "" {
			t.Fatal("accepted invalid credentials")
		}
	}
}

func TestOidcDeadlineAndNextOperation(t *testing.T) {
	type key struct{}
	calls := 0
	minted := 0
	now := time.Now().UTC().Truncate(time.Second)
	src := newOidcTokenSource(identityFunc(func(ctx context.Context, a string) (string, error) {
		minted++
		if ctx.Value(key{}) != "operation" {
			t.Fatal("lost trusted context")
		}
		return "broker-secret", nil
	}), "arn:aws:iam::123456789012:role/test", "us-east-1", "cluster", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if _, ok := r.Context().Deadline(); !ok {
				t.Fatal("missing deadline")
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return response(r, 200, fmt.Sprintf(`<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>key</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`, now.Add(time.Hour).Format(time.RFC3339))), nil
	}), func() time.Time { return now })
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), key{}, "operation"), 30*time.Millisecond)
	defer cancel()
	if _, _, err := src.Token(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline cause %v", err)
	}
	if tok, _, err := src.Token(context.WithValue(context.Background(), key{}, "operation")); err != nil || !strings.HasPrefix(tok, "k8s-aws-v1.") {
		t.Fatalf("next operation %q %v", tok, err)
	}
	if calls != 2 || minted != 2 {
		t.Fatalf("calls %d minted %d", calls, minted)
	}
}

func TestOidcCanceledCallerCannotMint(t *testing.T) {
	broker := identityFunc(func(context.Context, string) (string, error) {
		t.Fatal("minted for canceled operation")
		return "", nil
	})
	src := NewOidcTokenSource(broker, "arn:aws:iam::123456789012:role/test", "us-east-1", "cluster")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if token, _, err := src.Token(ctx); token != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled result %q %v", token, err)
	}
}
