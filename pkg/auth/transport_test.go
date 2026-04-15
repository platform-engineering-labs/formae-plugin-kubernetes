// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
)

type staticTokenSource struct {
	token string
}

func (s *staticTokenSource) Token(_ context.Context) (string, time.Time, error) {
	return s.token, time.Now().Add(1 * time.Hour), nil
}

func TestTokenTransport_InjectsAuthHeader(t *testing.T) {
	var capturedHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	source := &staticTokenSource{token: "test-token-123"}
	cached := auth.NewCachedTokenSource(source)
	transport := auth.NewTokenTransport(http.DefaultTransport, cached)

	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	resp.Body.Close()

	if capturedHeader != "Bearer test-token-123" {
		t.Errorf("expected 'Bearer test-token-123', got %q", capturedHeader)
	}
}
