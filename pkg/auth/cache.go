// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

const tokenSafetyMargin = 10 * time.Second

type cachedToken struct {
	token     string
	expiry    time.Time
	nextCheck time.Time
}

// CachedTokenSource coalesces synchronous refreshes without retaining any
// operation context. A canceled refresh leader cannot poison live waiters.
// Tokens are checked in their last 20% of life (at least 60 seconds early).
// An unchanged token without a later expiry is rechecked after 10 seconds.
// Renewed expiry uses the normal margin; no token is served in its final 10 seconds.
type CachedTokenSource struct {
	inner      TokenSource
	now        func() time.Time
	mu         sync.Mutex
	current    *cachedToken
	refreshing chan struct{}
	generation uint64
}

func NewCachedTokenSource(inner TokenSource) *CachedTokenSource {
	return &CachedTokenSource{inner: inner, now: time.Now}
}

// Invalidate also fences off a refresh already in flight. Its result must not
// resurrect credentials invalidated by a concurrent API rejection.
func (c *CachedTokenSource) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = nil
	c.generation++
}

func (c *CachedTokenSource) Token(ctx context.Context) (string, time.Time, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", time.Time{}, err
		}
		c.mu.Lock()
		now := c.now()
		if snap := c.current; snap != nil && now.Before(snap.nextCheck) && now.Before(snap.expiry.Add(-tokenSafetyMargin)) {
			c.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return "", time.Time{}, err
			}
			return snap.token, snap.expiry, nil
		}
		if done := c.refreshing; done != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", time.Time{}, ctx.Err()
			case <-done:
				continue
			}
		}
		done := make(chan struct{})
		c.refreshing = done
		generation, previous := c.generation, c.current
		c.mu.Unlock()

		// The leader executes on its caller's stack, under that caller's lifetime.
		var token string
		var expiry time.Time
		err := ctx.Err()
		if err == nil {
			token, expiry, err = c.inner.Token(ctx)
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		c.mu.Lock()
		now = c.now()
		if err == nil && (token == "" || expiry.IsZero() || expiry.Year() > 9999 || !now.Before(expiry.Add(-tokenSafetyMargin))) {
			err = errors.New("invalid or expired bearer token")
		}
		invalidated := generation != c.generation
		if err == nil && !invalidated {
			margin := expiry.Sub(now) / 5
			if margin < 60*time.Second {
				margin = 60 * time.Second
			}
			next := expiry.Add(-margin)
			sameTokenWithoutLaterExpiry := previous != nil && token == previous.token && !expiry.After(previous.expiry)
			if !next.After(now) || sameTokenWithoutLaterExpiry {
				next = now.Add(10 * time.Second)
			}
			if cutoff := expiry.Add(-tokenSafetyMargin); next.After(cutoff) {
				next = cutoff
			}
			c.current = &cachedToken{token: token, expiry: expiry, nextCheck: next}
		}
		// Early refresh is opportunistic until the safety cutoff. Failed leaders
		// may reuse only this generation's token and only for a live caller.
		if err != nil && ctx.Err() == nil && !invalidated && previous != nil && now.Before(previous.expiry.Add(-tokenSafetyMargin)) {
			next := minTime(now.Add(10*time.Second), previous.expiry.Add(-tokenSafetyMargin))
			c.current = &cachedToken{token: previous.token, expiry: previous.expiry, nextCheck: next}
			token, expiry, err = previous.token, previous.expiry, nil
		}
		c.refreshing = nil
		close(done)
		c.mu.Unlock()
		if err != nil {
			return "", time.Time{}, RedactError(err)
		}
		if invalidated {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", time.Time{}, err
		}
		return token, expiry, nil
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// RedactError hides credential-bearing messages while preserving errors.Is
// identity, including wrapped cancellation and authentication errors. It returns
// nil for nil. The raw cause is deliberately not exposed through Unwrap.
func RedactError(err error) error {
	if err == nil {
		return nil
	}
	return &redactedError{cause: err}
}

type redactedError struct{ cause error }

func (*redactedError) Error() string { return "authentication request failed" }
func (e *redactedError) Is(target error) bool {
	return errors.Is(e.cause, target) || target == ErrTransient && IsTransient(e.cause)
}
