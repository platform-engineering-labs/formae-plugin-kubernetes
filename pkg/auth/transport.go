// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// TokenSource produces bearer tokens on demand. It is the narrow contract that
// CachedTokenSource wraps — any AuthProvider satisfies it.
type TokenSource interface {
	Token(ctx context.Context) (token string, expiry time.Time, err error)
}

// cachedToken is the immutable snapshot we publish through atomic.Value.
type cachedToken struct {
	token    string
	issuedAt time.Time
	expiry   time.Time
}

// CachedTokenSource wraps a TokenSource and caches the token until the last
// 20 % of its TTL (with a hard floor of 60s), at which point the next call
// transparently refreshes.
//
// Concurrent callers do not serialize on a mutex during the network refresh;
// a singleflight.Group coalesces concurrent refreshes so only one inflight
// call hits the upstream IdP. This prevents a stuck IdP from wedging every
// concurrent K8s API request behind a single mutex.
type CachedTokenSource struct {
	inner TokenSource

	current atomic.Value // *cachedToken
	group   singleflight.Group
}

// NewCachedTokenSource returns a CachedTokenSource that delegates to inner.
func NewCachedTokenSource(inner TokenSource) *CachedTokenSource {
	return &CachedTokenSource{inner: inner}
}

// Token returns the cached bearer token, refreshing it when the remaining
// lifetime drops below 20 % of the original TTL (or 60s, whichever is larger).
//
// Token does not hold any lock while the upstream IdP is contacted; concurrent
// refreshes are coalesced via singleflight so only one goroutine performs the
// network round-trip.
func (c *CachedTokenSource) Token(ctx context.Context) (string, time.Time, error) {
	if snap := c.load(); snap != nil && !needsRefresh(snap, time.Now()) {
		return snap.token, snap.expiry, nil
	}
	return c.refresh(ctx)
}

// Invalidate forces the next Token call to refresh from the upstream source.
// Intended for use after a 401 response from the apiserver.
func (c *CachedTokenSource) Invalidate() {
	c.current.Store((*cachedToken)(nil))
}

func (c *CachedTokenSource) load() *cachedToken {
	v := c.current.Load()
	if v == nil {
		return nil
	}
	t, _ := v.(*cachedToken)
	return t
}

// refresh fetches a new token. singleflight coalesces concurrent callers
// onto the same upstream call. After the call returns, every waiter reads
// the result from the channel and returns to its caller. Only one goroutine
// holds the network connection at a time.
func (c *CachedTokenSource) refresh(ctx context.Context) (string, time.Time, error) {
	v, err, _ := c.group.Do("token", func() (interface{}, error) {
		// Double-checked: another goroutine may have refreshed between
		// the load() in Token() and our singleflight win.
		if snap := c.load(); snap != nil && !needsRefresh(snap, time.Now()) {
			return snap, nil
		}
		token, expiry, err := c.inner.Token(ctx)
		if err != nil {
			return nil, err
		}
		snap := &cachedToken{
			token:    token,
			issuedAt: time.Now(),
			expiry:   expiry,
		}
		c.current.Store(snap)
		return snap, nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	snap := v.(*cachedToken)
	return snap.token, snap.expiry, nil
}

// needsRefresh reports whether the cached token should be refreshed.
// Returns true when the remaining lifetime is less than 20 % of the total
// TTL, with a floor of 60s to absorb clock skew across the network.
func needsRefresh(snap *cachedToken, now time.Time) bool {
	totalTTL := snap.expiry.Sub(snap.issuedAt)
	if totalTTL <= 0 {
		return true
	}
	remaining := snap.expiry.Sub(now)
	threshold := totalTTL / 5
	if threshold < 60*time.Second {
		threshold = 60 * time.Second
	}
	return remaining < threshold
}

// invalidator is the narrow interface TokenTransport uses to flush the cached
// token after a 401. Only *CachedTokenSource implements it; other TokenSources
// will skip the retry branch.
type invalidator interface {
	Invalidate()
}

// TokenTransport is an http.RoundTripper that injects a bearer token from a
// TokenSource into every outgoing request's Authorization header.
//
// On a 401 response with `WWW-Authenticate: Bearer error="invalid_token"`
// (or, conservatively, any 401), the transport invalidates the cached token
// (if the source supports it) and retries the request once with a freshly
// minted token. This recovers from server-side revocation without waiting for
// the natural TTL to expire.
type TokenTransport struct {
	base   http.RoundTripper
	source TokenSource
}

// NewTokenTransport wraps base with bearer-token injection from source.
func NewTokenTransport(base http.RoundTripper, source TokenSource) *TokenTransport {
	return &TokenTransport{base: base, source: source}
}

// RoundTrip obtains a token and attaches it as a Bearer Authorization header,
// then delegates to the underlying transport. On a 401 it invalidates the
// cached token and retries once.
func (t *TokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	// Only retry when the source supports cache invalidation; without it
	// the next attempt would just reuse the same stale token.
	inv, ok := t.source.(invalidator)
	if !ok {
		return resp, nil
	}
	// Body must be consumed/closed before reusing the request. The 401
	// body is informational only (server-defined error description); we
	// have no use for it here and discard the close error too — any
	// failure to close affects only the idle-connection pool, not the
	// retry's correctness.
	_ = resp.Body.Close()
	inv.Invalidate()

	// One retry attempt with a fresh token.
	return t.do(req)
}

func (t *TokenTransport) do(req *http.Request) (*http.Response, error) {
	token, _, err := t.source.Token(req.Context())
	if err != nil {
		return nil, fmt.Errorf("auth: failed to obtain token: %w", err)
	}

	// Clone the request to avoid mutating the caller's original.
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(r)
}

