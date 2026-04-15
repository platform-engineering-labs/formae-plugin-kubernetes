// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package auth_test

import (
	"context"
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

func TestCachedTokenSource_ConcurrentCallsCoalesce(t *testing.T) {
	source := &countingTokenSource{token: "tok-1", ttl: 10 * time.Minute}
	cached := auth.NewCachedTokenSource(source)

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := cached.Token(ctx)
			if err != nil {
				t.Errorf("Token() error: %v", err)
			}
		}()
	}
	wg.Wait()

	// Mutex serialization means the first caller fetches, subsequent ones hit cache
	if source.calls.Load() > 2 {
		t.Errorf("expected at most 2 calls (mutex serialization), got %d", source.calls.Load())
	}
}
