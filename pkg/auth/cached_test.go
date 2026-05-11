// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
)

type countingTokenSource struct {
	calls atomic.Int64
	token string
	ttl   time.Duration
}

func (c *countingTokenSource) Token(_ context.Context) (string, time.Time, error) {
	c.calls.Add(1)
	return c.token, time.Now().Add(c.ttl), nil
}

func TestCachedTokenSource_CachesWithinTTL(t *testing.T) {
	source := &countingTokenSource{token: "tok-1", ttl: 10 * time.Minute}
	cached := auth.NewCachedTokenSource(source)

	ctx := context.Background()
	tok1, _, err := cached.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tok2, _, err := cached.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if tok1 != "tok-1" || tok2 != "tok-1" {
		t.Errorf("expected tok-1, got %q and %q", tok1, tok2)
	}
	if source.calls.Load() != 1 {
		t.Errorf("expected 1 call to source, got %d", source.calls.Load())
	}
}

func TestCachedTokenSource_RefreshesAfterExpiry(t *testing.T) {
	source := &countingTokenSource{token: "tok-1", ttl: 1 * time.Millisecond}
	cached := auth.NewCachedTokenSource(source)

	ctx := context.Background()
	_, _, _ = cached.Token(ctx)
	time.Sleep(5 * time.Millisecond) // let token expire
	_, _, _ = cached.Token(ctx)

	if source.calls.Load() != 2 {
		t.Errorf("expected 2 calls to source after expiry, got %d", source.calls.Load())
	}
}

// slowTokenSource blocks Token() until release is closed. Used to verify
// singleflight coalesces concurrent refreshes.
type slowTokenSource struct {
	calls   atomic.Int64
	release chan struct{}
	token   string
}

func (s *slowTokenSource) Token(_ context.Context) (string, time.Time, error) {
	s.calls.Add(1)
	<-s.release
	return s.token, time.Now().Add(1 * time.Hour), nil
}

func TestCachedTokenSource_ConcurrentRefreshesCoalesce(t *testing.T) {
	// 50 concurrent callers, one in-flight refresh that blocks until we
	// release it. Singleflight must collapse all 50 onto a single upstream
	// call.
	source := &slowTokenSource{release: make(chan struct{}), token: "tok-1"}
	cached := auth.NewCachedTokenSource(source)

	const N = 50
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := cached.Token(context.Background())
			if err != nil {
				errs <- err
			}
		}()
	}

	// Give all goroutines a moment to enqueue, then release the blocked
	// upstream call. 50 ms is well under any plausible CI scheduler stall.
	time.Sleep(50 * time.Millisecond)
	close(source.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Token() error: %v", err)
	}

	// With singleflight, exactly one upstream call should have happened.
	if got := source.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 upstream call (singleflight coalesce), got %d", got)
	}
}

func TestCachedTokenSource_Invalidate(t *testing.T) {
	source := &countingTokenSource{token: "tok-1", ttl: 1 * time.Hour}
	cached := auth.NewCachedTokenSource(source)

	ctx := context.Background()
	_, _, _ = cached.Token(ctx)
	cached.Invalidate()
	_, _, _ = cached.Token(ctx)

	if got := source.calls.Load(); got != 2 {
		t.Errorf("expected 2 calls after invalidate, got %d", got)
	}
}

// errorTokenSource always returns an error. Used to verify Token() propagates
// errors and does not cache failed responses.
type errorTokenSource struct {
	calls atomic.Int64
	err   error
}

func (e *errorTokenSource) Token(_ context.Context) (string, time.Time, error) {
	e.calls.Add(1)
	return "", time.Time{}, e.err
}

func TestCachedTokenSource_DoesNotCacheErrors(t *testing.T) {
	source := &errorTokenSource{err: errors.New("transient")}
	cached := auth.NewCachedTokenSource(source)

	ctx := context.Background()
	if _, _, err := cached.Token(ctx); err == nil {
		t.Fatal("expected error from first Token call")
	}
	if _, _, err := cached.Token(ctx); err == nil {
		t.Fatal("expected error from second Token call")
	}

	if got := source.calls.Load(); got < 2 {
		t.Errorf("expected at least 2 upstream calls when source errors, got %d", got)
	}
}

// ctxTokenSource respects context cancellation. Used to verify that a
// cancelled context propagates through Token().
type ctxTokenSource struct{}

func (c *ctxTokenSource) Token(ctx context.Context) (string, time.Time, error) {
	<-ctx.Done()
	return "", time.Time{}, ctx.Err()
}

func TestCachedTokenSource_RespectsContextCancellation(t *testing.T) {
	cached := auth.NewCachedTokenSource(&ctxTokenSource{})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := cached.Token(ctx)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	// Should return within ~50ms; allow generous slack for CI.
	if elapsed > 500*time.Millisecond {
		t.Errorf("expected fast return on context timeout, took %v", elapsed)
	}
}

// invalidatingTokenSource counts Token calls and lets a test mutate the
// returned token so we can verify TokenTransport rotates on 401.
type invalidatingTokenSource struct {
	calls atomic.Int64
	mu    sync.Mutex
	token string
}

func (s *invalidatingTokenSource) Token(_ context.Context) (string, time.Time, error) {
	s.calls.Add(1)
	s.mu.Lock()
	tok := s.token
	s.mu.Unlock()
	return tok, time.Now().Add(1 * time.Hour), nil
}

func TestTokenTransport_RetriesOn401_WithInvalidation(t *testing.T) {
	// Server returns 401 on the first call and 200 on the second.
	var calls atomic.Int64
	var headers []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get("Authorization"))
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	source := &invalidatingTokenSource{token: "stale"}
	cached := auth.NewCachedTokenSource(source)

	// Prime the cache so the first request uses "stale", then mutate the
	// source so a forced refresh after 401 returns "fresh".
	if _, _, err := cached.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	source.token = "fresh"
	source.mu.Unlock()

	transport := auth.NewTokenTransport(http.DefaultTransport, cached)
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 server calls (initial + retry), got %d", calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 2 {
		t.Fatalf("expected 2 captured headers, got %d", len(headers))
	}
	if headers[0] != "Bearer stale" {
		t.Errorf("first header should use stale token, got %q", headers[0])
	}
	if headers[1] != "Bearer fresh" {
		t.Errorf("second header should use fresh token, got %q", headers[1])
	}
}

func TestTokenTransport_PassesThroughNon401(t *testing.T) {
	// Any non-401 response should pass through with no retry.
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	source := &invalidatingTokenSource{token: "tok"}
	cached := auth.NewCachedTokenSource(source)
	transport := auth.NewTokenTransport(http.DefaultTransport, cached)

	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 passthrough, got %d", resp.StatusCode)
	}
	if calls.Load() != 1 {
		t.Errorf("expected exactly 1 server call (no retry), got %d", calls.Load())
	}
}
