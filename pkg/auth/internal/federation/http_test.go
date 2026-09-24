// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package federation

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientRejectsCredentialEgressOutsideExactEndpoint(t *testing.T) {
	client := Client(roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("forbidden credential egress"); return nil, nil }), "https://exchange.example/token")
	for _, url := range []string{"http://exchange.example/token", "https://other.example/token", "https://exchange.example/other", "https://exchange.example:443/token", "https://user:pass@exchange.example/token", "https://exchange.example/token?redirect=attacker", "https://exchange.example/token#fragment"} {
		req, err := http.NewRequest("POST", url, strings.NewReader("assertion=secret"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = client.Do(req); err == nil {
			t.Errorf("accepted %s", url)
		}
	}
	req, _ := http.NewRequest("POST", "https://exchange.example/token", nil)
	req.Host = "attacker.example"
	if _, err := client.Do(req); err == nil {
		t.Fatal("accepted Host override")
	}
	req, _ = http.NewRequest("POST", "https://exchange.example/token", nil)
	req.Response = &http.Response{StatusCode: 307}
	if _, err := client.Do(req); err == nil {
		t.Fatal("accepted redirected request")
	}
}

func TestClientRejectsEveryRedirectIncludingSameOrigin(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, destination := range []string{"https://exchange.example/token", "https://attacker.example/token"} {
			calls := 0
			client := Client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls != 1 {
					t.Fatal("credential request followed redirect")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{destination}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			}), "https://exchange.example/token")
			req, _ := http.NewRequest("POST", "https://exchange.example/token", strings.NewReader("assertion=secret"))
			req.Header.Set("Authorization", "Bearer secret")
			res, err := client.Do(req)
			if res != nil {
				res.Body.Close()
			}
			if err == nil {
				t.Fatal("accepted redirect")
			}
		}
	}
}
