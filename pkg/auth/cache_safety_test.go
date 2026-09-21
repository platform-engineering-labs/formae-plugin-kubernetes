//go:build unit

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type tokenFunc func(context.Context) (string, time.Time, error)

func (f tokenFunc) Token(ctx context.Context) (string, time.Time, error) { return f(ctx) }

// Done observation is a handshake that the live caller reached the cache's
// cancelable wait, so the leader-cancellation test cannot pass by scheduling
// the waiter only after the leader has exited.
type observedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

// Rejecting unsafe expiries prevents serving an expired token even on the first mint.
func TestCacheRejectsUnsafeTokens(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, token string
		expiry      time.Time
	}{
		{"zero", "secret", time.Time{}}, {"expired", "secret", now.Add(-time.Second)},
		{"cutoff", "secret", now.Add(10 * time.Second)}, {"empty", "", now.Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) { return tc.token, tc.expiry, nil }))
			tok, _, err := c.Token(context.Background())
			if err == nil || tok != "" {
				t.Fatalf("unsafe token accepted: token=%q err=%v", tok, err)
			}
		})
	}
}

func TestCacheCanceledContextNeverMintsOrReadsCachedToken(t *testing.T) {
	var calls int
	c := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
		calls++
		return "token", time.Now().Add(time.Hour), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Token(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled empty cache: %v", err)
	}
	if calls != 0 {
		t.Fatal("mint after cancellation")
	}
	if _, _, err := c.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Token(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled warm cache: %v", err)
	}
	if calls != 1 {
		t.Fatal("canceled caller minted")
	}
}

func TestCacheCanceledLeaderDoesNotPoisonLiveWaiter(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int64
	c := NewCachedTokenSource(tokenFunc(func(ctx context.Context) (string, time.Time, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return "", time.Time{}, ctx.Err()
		}
		return "live", time.Now().Add(time.Hour), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := make(chan error, 1)
	go func() { _, _, err := c.Token(ctx); leader <- err }()
	<-started
	waiter := make(chan error, 1)
	live := &observedContext{Context: context.Background(), entered: make(chan struct{})}
	go func() {
		tok, _, err := c.Token(live)
		if err == nil && tok != "live" {
			err = fmt.Errorf("wrong token %q", tok)
		}
		waiter <- err
	}()
	select {
	case <-live.entered:
	case <-time.After(time.Second):
		t.Fatal("live caller did not enter cancelable wait")
	}
	cancel()
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader: %v", err)
	}
	select {
	case err := <-waiter:
		if err != nil {
			t.Fatalf("live waiter: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("live waiter stuck")
	}
}

func TestCacheWaiterCancelsIndependently(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	c := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
		close(started)
		<-release
		return "token", time.Now().Add(time.Hour), nil
	}))
	leader := make(chan struct{})
	go func() { defer close(leader); _, _, _ = c.Token(context.Background()) }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, _, err := c.Token(ctx); result <- err }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Error("waiter blocked behind leader")
	}
}

func TestCacheInvalidationDuringRefreshDiscardsOldResult(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	c := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return "invalidated", time.Now().Add(time.Hour), nil
		}
		return "fresh", time.Now().Add(time.Hour), nil
	}))
	done := make(chan string, 1)
	go func() { tok, _, _ := c.Token(context.Background()); done <- tok }()
	<-started
	c.Invalidate()
	close(release)
	if tok := <-done; tok != "fresh" {
		t.Fatalf("returned invalidated in-flight token %q", tok)
	}
	tok, _, err := c.Token(context.Background())
	if err != nil || tok != "fresh" {
		t.Fatalf("cache poisoned: %q %v", tok, err)
	}
}

func TestCacheRedactsSourceFailures(t *testing.T) {
	sentinel := errors.New("authentication denied")
	raw := fmt.Errorf("assertion=SECRET signed=https://example.invalid/?sig=SECRET: %w", sentinel)
	c := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) { return "", time.Time{}, raw }))
	_, _, err := c.Token(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "https://") {
		t.Fatalf("unsafe error: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatal("auth error identity lost")
	}
}

func TestCacheBrokerReuseClockSkewAndCutoff(t *testing.T) {
	for _, skew := range []time.Duration{-20 * time.Second, 0, 20 * time.Second} {
		t.Run(skew.String(), func(t *testing.T) {
			issuerNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			now := issuerNow.Add(skew)
			expiry := issuerNow.Add(300 * time.Second)
			calls := 0
			fail := false
			returned := "first"
			c := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
				calls++
				if fail {
					return "", time.Time{}, errors.New("mandatory refresh failed")
				}
				return returned, expiry, nil
			}))
			c.now = func() time.Time { return now }
			get := func(want string, wantCalls int) {
				t.Helper()
				tok, _, err := c.Token(context.Background())
				if err != nil || tok != want || calls != wantCalls {
					t.Fatalf("at %v token=%q calls=%d err=%v; want %q calls=%d", now, tok, calls, err, want, wantCalls)
				}
			}
			get("first", 1)
			// The initial refresh threshold is 20% of remaining life or 60 seconds.
			threshold := 240 * time.Second
			if skew < 0 {
				threshold = 236 * time.Second
			}
			now = issuerNow.Add(threshold)
			get("first", 2)
			now = now.Add(9 * time.Second)
			get("first", 2)
			now = now.Add(time.Second)
			get("first", 3)
			// A different token with the same exp must replace the old bytes.
			returned = "rotated"
			now = now.Add(10 * time.Second)
			get("rotated", 4)
			now = issuerNow.Add(289 * time.Second)
			get("rotated", 5)
			now = issuerNow.Add(290 * time.Second)
			if tok, _, err := c.Token(context.Background()); err == nil || tok != "" {
				t.Fatalf("served at safety cutoff: %q %v", tok, err)
			}
			// Failed mandatory refresh must never resurrect the cached token.
			fail = true
			if tok, _, err := c.Token(context.Background()); err == nil || tok != "" {
				t.Fatal("failed refresh used stale token")
			}
		})
	}
}

func TestRedactErrorPreservesIdentityAndNil(t *testing.T) {
	if RedactError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("auth rejected")} {
		err := RedactError(fmt.Errorf("SECRET https://signed.invalid/?sig=SECRET: %w", cause))
		if !errors.Is(err, cause) || strings.Contains(fmt.Sprintf("%+v", err), "SECRET") {
			t.Fatalf("unsafe or untyped error: %v", err)
		}
	}
}

// A broker call that finishes after cancellation must neither publish nor return
// its result, even when the source cannot interrupt its synchronous work.
func TestCacheCanceledLeaderDiscardsSuccessfulMint(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	c := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return "canceled", time.Now().Add(time.Hour), nil
		}
		return "live", time.Now().Add(time.Hour), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, _, err := c.Token(ctx); result <- err }()
	<-started
	cancel()
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mint returned success: %v", err)
	}
	if tok, _, err := c.Token(context.Background()); err != nil || tok != "live" {
		t.Fatalf("canceled mint poisoned cache: %q %v", tok, err)
	}
}

func TestCacheFailedEarlyRefreshUsesTokenOnlyBeforeCutoff(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	expiry := start.Add(300 * time.Second)
	calls := 0
	c := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
		calls++
		if calls > 1 {
			return "", time.Time{}, errors.New("refresh unavailable")
		}
		return "cached", expiry, nil
	}))
	c.now = func() time.Time { return now }
	for _, tc := range []struct{ seconds, calls int }{{0, 1}, {240, 2}, {249, 2}, {250, 3}, {289, 4}} {
		now = start.Add(time.Duration(tc.seconds) * time.Second)
		tok, exp, err := c.Token(context.Background())
		if err != nil || tok != "cached" || !exp.Equal(expiry) || calls != tc.calls {
			t.Fatalf("at %d: token=%q expiry=%v calls=%d err=%v", tc.seconds, tok, exp, calls, err)
		}
		if c.current.nextCheck.After(expiry.Add(-10*time.Second)) || c.current.nextCheck.After(now.Add(10*time.Second)) && tc.seconds >= 240 {
			t.Fatal("unbounded retry time")
		}
	}
	now = start.Add(290 * time.Second)
	if tok, _, err := c.Token(context.Background()); err == nil || tok != "" {
		t.Fatalf("cutoff fallback: %q %v", tok, err)
	}
}

func TestCacheFailedEarlyRefreshCannotResurrectInvalidatedOrCanceledToken(t *testing.T) {
	for _, mode := range []string{"invalidate", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			started, release := make(chan struct{}), make(chan struct{})
			calls := 0
			c := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
				calls++
				if calls == 1 {
					return "old", now.Add(300 * time.Second), nil
				}
				close(started)
				<-release
				return "", time.Time{}, errors.New("refresh unavailable")
			}))
			c.now = func() time.Time { return now }
			_, _, _ = c.Token(context.Background())
			now = now.Add(240 * time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan string, 1)
			go func() { tok, _, _ := c.Token(ctx); result <- tok }()
			<-started
			if mode == "invalidate" {
				c.Invalidate()
			} else {
				cancel()
			}
			close(release)
			if tok := <-result; tok != "" {
				t.Fatalf("%s returned %q", mode, tok)
			}
		})
	}
}

// OVH's kubeconfig source returns a synthetic 30-minute revalidation TTL, not
// the credential's expiry. Clamping equal token bytes to their first TTL would
// permanently reject a successfully revalidated opaque token after 30 minutes.
func TestCacheHonorsRollingExpiryForUnchangedOpaqueToken(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	calls := 0
	c := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
		calls++
		return "unchanged-opaque-token", now.Add(30 * time.Minute), nil
	}))
	c.now = func() time.Time { return now }
	for _, tc := range []struct {
		elapsed      time.Duration
		expiryMinute int
		calls        int
	}{
		{0, 30, 1},
		{24 * time.Minute, 54, 2},
		{31 * time.Minute, 54, 2},
		{61 * time.Minute, 91, 3},
	} {
		now = start.Add(tc.elapsed)
		token, expiry, err := c.Token(context.Background())
		if err != nil || token != "unchanged-opaque-token" {
			t.Fatalf("at %v: revalidated opaque token rejected: %q %v", tc.elapsed, token, err)
		}
		if !expiry.Equal(start.Add(time.Duration(tc.expiryMinute)*time.Minute)) || calls != tc.calls {
			t.Errorf("at %v: expiry=%v calls=%d; want expiry at minute %d calls=%d", tc.elapsed, expiry, calls, tc.expiryMinute, tc.calls)
		}
	}
}

func TestCacheEarlyRefreshFailureCoalescesLiveWaiters(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	cache := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
		if calls.Add(1) == 1 {
			return "cached", now.Add(300 * time.Second), nil
		}
		close(started)
		<-release
		return "", time.Time{}, errors.New("temporary failure")
	}))
	cache.now = func() time.Time { return now }
	if _, _, err := cache.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(240 * time.Second)
	done := make(chan error, 20)
	go func() { _, _, err := cache.Token(context.Background()); done <- err }()
	<-started
	for i := 0; i < 19; i++ {
		go func() {
			token, _, err := cache.Token(context.Background())
			if err == nil && token != "cached" {
				err = errors.New("wrong fallback")
			}
			done <- err
		}()
	}
	close(release)
	for i := 0; i < 20; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("failed early refresh was not coalesced")
	}
}
