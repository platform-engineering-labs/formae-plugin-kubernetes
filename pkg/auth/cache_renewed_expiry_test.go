//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"testing"
	"time"
)

func TestCacheUnchangedOpaqueTokenWithRenewedExpiryUsesNormalMargin(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	calls := 0
	cache := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
		calls++
		return "unchanged-opaque-token", now.Add(30 * time.Minute), nil
	}))
	cache.now = func() time.Time { return now }
	get := func(wantExpiry time.Time, wantCalls int) {
		t.Helper()
		token, expiry, err := cache.Token(context.Background())
		if err != nil || token != "unchanged-opaque-token" || !expiry.Equal(wantExpiry) || calls != wantCalls {
			t.Fatalf("at %v: token=%q expiry=%v calls=%d err=%v; want expiry=%v calls=%d", now.Sub(start), token, expiry, calls, err, wantExpiry, wantCalls)
		}
	}

	get(start.Add(30*time.Minute), 1)
	now = start.Add(24 * time.Minute)
	get(start.Add(54*time.Minute), 2)
	now = start.Add(24*time.Minute + 11*time.Second)
	get(start.Add(54*time.Minute), 2)
	now = start.Add(47*time.Minute + 59*time.Second)
	get(start.Add(54*time.Minute), 2)
	now = start.Add(48 * time.Minute)
	get(start.Add(78*time.Minute), 3)
}

func TestCacheUnchangedTokenWithoutLaterExpiryKeepsTenSecondRecheck(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		refreshExpiry time.Time
	}{
		{name: "equal", refreshExpiry: start.Add(30 * time.Minute)},
		{name: "earlier", refreshExpiry: start.Add(29 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := start
			expiry := start.Add(30 * time.Minute)
			calls := 0
			cache := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
				calls++
				if calls > 1 {
					expiry = tc.refreshExpiry
				}
				return "unchanged-token", expiry, nil
			}))
			cache.now = func() time.Time { return now }
			get := func(wantCalls int) {
				t.Helper()
				token, gotExpiry, err := cache.Token(context.Background())
				if err != nil || token != "unchanged-token" || !gotExpiry.Equal(expiry) || calls != wantCalls {
					t.Fatalf("at %v: token=%q expiry=%v calls=%d err=%v; want calls=%d", now.Sub(start), token, gotExpiry, calls, err, wantCalls)
				}
			}

			get(1)
			now = start.Add(24 * time.Minute)
			get(2)
			now = start.Add(24*time.Minute + 9*time.Second)
			get(2)
			now = start.Add(24*time.Minute + 10*time.Second)
			get(3)
		})
	}
}
