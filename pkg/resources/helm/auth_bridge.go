// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"k8s.io/utils/clock"
)

var errRefreshServiceStarved = errors.New("helm credential refresh service starved; no active operation supplied credentials within the polling budget")

// authBridge holds data and wakeups only. In particular it holds neither a
// token source nor a callback context: only Service's caller may mint.
type authBridge struct {
	mu               sync.Mutex
	clock            clock.Clock
	window           time.Duration
	allowance        time.Duration // immutable configured action budget
	deadline         time.Time
	serviced         time.Time
	token            string
	expiry           time.Time
	closed           error
	changed          chan struct{}
	requests         chan struct{}
	servicing        chan struct{}
	generation       uint64
	sourceGeneration uint64
}

func newAuthBridge(info plugin.OidcOperationInfo, authFingerprint string, allowance time.Duration, deadline time.Time) (*authBridge, error) {
	return newAuthBridgeWithClock(info, authFingerprint, allowance, deadline, clock.RealClock{})
}
func bridgeWindow(info plugin.OidcOperationInfo) (time.Duration, error) {
	if info.BindingID == "" || info.CallTimeout <= 0 || info.PollInterval < 0 || info.RetryDelay < 0 || info.ThrottleMaxDelay < 0 {
		return 0, errors.New("helm OIDC requires valid trusted operation metadata")
	}
	gap := max(info.PollInterval, info.RetryDelay, info.ThrottleMaxDelay)
	const reserve = 40 * time.Second
	if gap > math.MaxInt64-reserve-30*time.Second || info.CallTimeout > math.MaxInt64-reserve-30*time.Second-gap {
		return 0, errors.New("helm OIDC operation metadata timing overflows")
	}
	return info.CallTimeout + gap + reserve, nil
}
func validateActionAllowance(info plugin.OidcOperationInfo, allowance time.Duration) error {
	w, err := bridgeWindow(info)
	if err != nil {
		return err
	}
	if allowance < w {
		return fmt.Errorf("timeoutSeconds must allow at least %s for Helm OIDC refresh (effective poll interval %s)", w, info.PollInterval)
	}
	return nil
}
func newAuthBridgeWithClock(info plugin.OidcOperationInfo, fingerprint string, allowance time.Duration, deadline time.Time, c clock.Clock) (*authBridge, error) {
	if fingerprint == "" {
		return nil, errors.New("helm OIDC requires valid auth identity")
	}
	if err := validateActionAllowance(info, allowance); err != nil {
		return nil, err
	}
	now := c.Now()
	if !now.Before(deadline) {
		return nil, context.DeadlineExceeded
	}
	w, _ := bridgeWindow(info)
	return &authBridge{clock: c, window: w, allowance: allowance, deadline: deadline, serviced: now, changed: make(chan struct{}), requests: make(chan struct{}, 1), servicing: make(chan struct{}, 1)}, nil
}

func (b *authBridge) notifyLocked() { close(b.changed); b.changed = make(chan struct{}) }
func (b *authBridge) requestLocked() {
	select {
	case b.requests <- struct{}{}:
	default:
	}
}
func (b *authBridge) Token(ctx context.Context) (string, time.Time, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", time.Time{}, err
		}
		b.mu.Lock()
		now := b.clock.Now()
		if b.closed != nil {
			err := b.closed
			b.mu.Unlock()
			return "", time.Time{}, err
		}
		limit := b.serviced.Add(b.window)
		if b.deadline.Before(limit) {
			limit = b.deadline
		}
		if !now.Before(limit) {
			b.mu.Unlock()
			return "", time.Time{}, errRefreshServiceStarved
		}
		if b.token != "" && now.Before(b.expiry.Add(-10*time.Second)) {
			token, expiry := b.token, b.expiry
			b.mu.Unlock()
			return token, expiry, nil
		}
		b.requestLocked()
		changed := b.changed
		timer := b.clock.NewTimer(limit.Sub(now))
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", time.Time{}, ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C():
		}
	}
}

// Service runs synchronously on a live handler's stack. It proactively refreshes
// even with no queued worker, so a lazy Helm client cannot deadlock Status.
func (b *authBridge) Service(ctx context.Context, source auth.TokenSource) error {
	if source == nil {
		return errors.New("helm credential source unavailable")
	}
	service, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case b.servicing <- struct{}{}:
	case <-service.Done():
		return service.Err()
	}
	defer func() { <-b.servicing }()
	if err := service.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed != nil {
		err := b.closed
		b.mu.Unlock()
		return err
	}
	generation := b.generation
	invalidate := generation != b.sourceGeneration
	b.sourceGeneration = generation
	b.mu.Unlock()
	// Worker 401 invalidation must also bypass the shared callback cache. The
	// bridge keeps only a generation counter, never the live source itself.
	if invalidate {
		if cached, ok := source.(interface{ Invalidate() }); ok {
			cached.Invalidate()
		}
	}
	token, expiry, err := source.Token(service)
	if service.Err() != nil {
		err = service.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed != nil {
		return b.closed
	}
	if err == nil && (token == "" || expiry.IsZero() || expiry.Year() > 9999 || !b.clock.Now().Before(expiry.Add(-10*time.Second))) {
		err = errors.New("invalid or expired bearer token")
	}
	if err != nil {
		return auth.RedactError(err)
	}
	// A simultaneous 401 invalidation fences a refresh already in flight.
	if generation != b.generation {
		b.requestLocked()
		return nil
	}
	b.token, b.expiry, b.serviced = token, expiry, b.clock.Now()
	select {
	case <-b.requests:
	default:
	}
	b.notifyLocked()
	return nil
}
func (b *authBridge) Invalidate() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.token = ""
	b.expiry = time.Time{}
	b.generation++
	b.requestLocked()
	b.notifyLocked()
}
func (b *authBridge) Close(err error) {
	if err == nil {
		err = context.Canceled
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed != nil {
		return
	}
	b.closed = err
	b.token = ""
	b.expiry = time.Time{}
	b.notifyLocked()
}
