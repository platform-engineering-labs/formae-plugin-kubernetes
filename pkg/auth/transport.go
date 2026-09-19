// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TokenSource produces bearer tokens using the live caller's context.
type TokenSource interface {
	Token(ctx context.Context) (token string, expiry time.Time, err error)
}

type invalidator interface{ Invalidate() }

// TokenTransport injects a token into a cloned request and permits at most one
// replayable 401 retry. New OIDC callers must use NewOriginTokenTransport.
type TokenTransport struct {
	base       http.RoundTripper
	source     TokenSource
	origin     *url.URL
	restricted bool
}

// NewTokenTransport preserves the legacy unbound transport API.
func NewTokenTransport(base http.RoundTripper, source TokenSource) *TokenTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &TokenTransport{base: base, source: source}
}

// NewOriginTokenTransport permits bearer credentials only for the configured
// HTTPS origin. All redirected requests are rejected before obtaining a token,
// including redirects to that same origin. The origin is copied at construction.
func NewOriginTokenTransport(base http.RoundTripper, source TokenSource, origin *url.URL) http.RoundTripper {
	t := NewTokenTransport(base, source)
	t.restricted = true
	if origin != nil {
		copy := *origin
		t.origin = &copy
	}
	return t
}

func (t *TokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.restricted && (req.Response != nil || !sameOrigin(t.origin, req.URL) || (req.Host != "" && !strings.EqualFold(req.Host, req.URL.Host))) {
		closeRequest(req)
		return nil, errors.New("authentication transport rejected request origin or redirect")
	}
	resp, err := t.do(req)
	if err != nil {
		return nil, err
	}
	inv, ok := t.source.(invalidator)
	if resp.StatusCode != http.StatusUnauthorized || !ok || (req.Body != nil && req.Body != http.NoBody && req.GetBody == nil) {
		return resp, nil
	}
	retry := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		body, err := req.GetBody()
		if err != nil {
			return resp, nil
		}
		retry.Body = body
	}
	// Close only the response we discard; the final body belongs to the caller.
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	inv.Invalidate()
	return t.do(retry)
}

func (t *TokenTransport) do(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		closeRequest(req)
		return nil, err
	}
	token, _, err := t.source.Token(req.Context())
	if req.Context().Err() != nil {
		err = req.Context().Err()
	}
	if err != nil {
		closeRequest(req)
		return nil, RedactError(err)
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, RedactError(err)
	}
	return resp, nil
}

func closeRequest(req *http.Request) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
}
func sameOrigin(origin, target *url.URL) bool {
	if origin == nil || target == nil || origin.Scheme != "https" || target.Scheme != "https" || origin.Host == "" || target.Host == "" || origin.User != nil || target.User != nil {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		return "443"
	}
	return strings.EqualFold(origin.Hostname(), target.Hostname()) && port(origin) == port(target)
}
