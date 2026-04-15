// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// TokenSource produces bearer tokens on demand. It is the narrow contract that
// CachedTokenSource wraps — any AuthProvider satisfies it.
type TokenSource interface {
	Token(ctx context.Context) (token string, expiry time.Time, err error)
}

// CachedTokenSource wraps a TokenSource and caches the token until the last
// 20 % of its TTL, at which point the next call transparently refreshes.
//
// Storing issuedAt alongside expiry lets us compute the original TTL
// (totalTTL = expiry - issuedAt) so that the refresh threshold is always
// relative to the token's full lifetime.
type CachedTokenSource struct {
	inner TokenSource

	mu       sync.Mutex
	token    string
	issuedAt time.Time
	expiry   time.Time
}

// NewCachedTokenSource returns a CachedTokenSource that delegates to inner.
func NewCachedTokenSource(inner TokenSource) *CachedTokenSource {
	return &CachedTokenSource{inner: inner}
}

// Token returns the cached bearer token, refreshing it when the remaining
// lifetime drops below 20 % of the original TTL.
func (c *CachedTokenSource) Token(ctx context.Context) (string, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && !c.needsRefresh(time.Now()) {
		return c.token, c.expiry, nil
	}

	token, expiry, err := c.inner.Token(ctx)
	if err != nil {
		return "", time.Time{}, err
	}

	c.token = token
	c.issuedAt = time.Now()
	c.expiry = expiry
	return c.token, c.expiry, nil
}

// needsRefresh reports whether the cached token should be refreshed.
// It returns true when the remaining lifetime is less than 20 % of the
// total TTL (expiry - issuedAt).
func (c *CachedTokenSource) needsRefresh(now time.Time) bool {
	totalTTL := c.expiry.Sub(c.issuedAt)
	if totalTTL <= 0 {
		return true
	}
	remaining := c.expiry.Sub(now)
	return remaining < totalTTL/5
}

// TokenTransport is an http.RoundTripper that injects a bearer token from a
// TokenSource into every outgoing request's Authorization header.
type TokenTransport struct {
	base   http.RoundTripper
	source TokenSource
}

// NewTokenTransport wraps base with bearer-token injection from source.
func NewTokenTransport(base http.RoundTripper, source TokenSource) *TokenTransport {
	return &TokenTransport{base: base, source: source}
}

// RoundTrip obtains a token and attaches it as a Bearer Authorization header,
// then delegates to the underlying transport.
func (t *TokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, _, err := t.source.Token(req.Context())
	if err != nil {
		return nil, fmt.Errorf("auth: failed to obtain token: %w", err)
	}

	// Clone the request to avoid mutating the caller's original.
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(r)
}
