//go:build unit

package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const audience = "urn:formae:kubernetes:49c24d1d-3815-4817-9242-4032be46601b"

type kubernetesAudienceVector struct {
	Name     string `json:"name"`
	Audience string `json:"audience"`
	Valid    bool   `json:"valid"`
}

func TestDirectProviderKubernetesAudienceVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/kubernetes-audience-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []kubernetesAudienceVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			if got := validAudience(vector.Audience); got != vector.Valid {
				t.Fatalf("validAudience(%q) = %v, want %v", vector.Audience, got, vector.Valid)
			}
		})
	}
}

type identityFunc func(context.Context, string) (string, error)

func (f identityFunc) IdentityToken(ctx context.Context, a string) (string, error) { return f(ctx, a) }
func jwt(payload string) string {
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".c2lnbmF0dXJl"
}

func TestDirectProviderReturnsTokenAndExpiryUsingLiveBinding(t *testing.T) {
	type key struct{}
	expires := time.Now().Add(5 * time.Minute).Truncate(time.Second)
	token := jwt(fmt.Sprintf(`{"exp":%d}`, expires.Unix()))
	calls := 0
	provider := NewProvider(identityFunc(func(ctx context.Context, a string) (string, error) {
		calls++
		if a != audience || ctx.Value(key{}) != "live" {
			t.Error("lost audience or operation context")
		}
		return token, nil
	}), audience)
	actual, exp, err := provider.Token(context.WithValue(context.Background(), key{}, "live"))
	if err != nil || actual != token || !exp.Equal(expires) || calls != 1 {
		t.Fatalf("token=%q expiry=%v err=%v calls=%d", actual, exp, err, calls)
	}
}

func TestDirectProviderRejectsInvalidAudienceBeforeMint(t *testing.T) {
	calls := 0
	source := identityFunc(func(context.Context, string) (string, error) { calls++; return "SECRET", nil })
	for _, a := range []string{"", "urn:formae:kubernetes:*", "sts.amazonaws.com", strings.ToUpper(audience), "urn:formae:kubernetes:00000000-0000-0000-0000-000000000000", "urn:formae:kubernetes:49c24d1d-3815-0817-1242-4032be46601b", audience + "/"} {
		if tok, _, err := NewProvider(source, a).Token(context.Background()); err == nil || tok != "" {
			t.Errorf("accepted invalid audience %q", a)
		}
	}
	if calls != 0 {
		t.Fatal("invalid audience reached broker")
	}
}

func TestDirectProviderRejectsMalformedAndUnsafeJWT(t *testing.T) {
	future := time.Now().Add(5 * time.Minute).Unix()
	for _, token := range []string{
		"SECRET", "a.SECRET.c", jwt(`{}`), jwt(`{"exp":null}`), jwt(`{"exp":"SECRET"}`), jwt(`{"exp":1e1000}`), jwt(`{"exp":9223372036854775807}`), jwt(`{"exp":NaN}`), jwt(`{"exp":-1}`),
		jwt(fmt.Sprintf(`{"exp":%d}`, time.Now().Unix())), jwt(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(9*time.Second).Unix())),
		jwt(fmt.Sprintf(`{"exp":%d}SECRET`, future)),
	} {
		p := NewProvider(identityFunc(func(context.Context, string) (string, error) { return token, nil }), audience)
		tok, _, err := p.Token(context.Background())
		if err == nil || tok != "" {
			t.Errorf("accepted malformed JWT %q", token)
		} else if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), token) {
			t.Errorf("leaked token in error %v", err)
		}
	}
}

func TestDirectProviderRedactsErrorsAndHonorsCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("broker auth denied")} {
		p := NewProvider(identityFunc(func(context.Context, string) (string, error) {
			return "", fmt.Errorf("SECRET https://signed.invalid/?sig=SECRET: %w", cause)
		}), audience)
		_, _, err := p.Token(context.Background())
		if !errors.Is(err, cause) || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("unsafe or untyped error %v", err)
		}
	}
	calls := 0
	p := NewProvider(identityFunc(func(context.Context, string) (string, error) { calls++; return "SECRET", nil }), audience)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := p.Token(ctx)
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("minted after cancellation: calls=%d err=%v", calls, err)
	}
	if _, _, err := NewProvider(nil, audience).Token(context.Background()); err == nil {
		t.Fatal("nil broker accepted")
	}
}

func TestDirectProviderRejectsBrokenJWTEnvelopeAndNoncanonicalExpiryClaim(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(5*time.Minute).Unix())))
	for _, token := range []string{"SECRET." + payload + ".c2ln", "e30." + payload + ".SECRET!", jwt(fmt.Sprintf(`{"EXP":%d}`, time.Now().Add(5*time.Minute).Unix())), jwt(fmt.Sprintf(`{"exp":"%d"}`, time.Now().Add(5*time.Minute).Unix()))} {
		p := NewProvider(identityFunc(func(context.Context, string) (string, error) { return token, nil }), audience)
		if tok, _, err := p.Token(context.Background()); err == nil || tok != "" {
			t.Errorf("malformed envelope accepted: %q", token)
		}
	}
}
