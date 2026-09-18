//go:build unit

package helm

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
	clocktesting "k8s.io/utils/clock/testing"
)

type bridgeSource func(context.Context) (string, time.Time, error)

func (f bridgeSource) Token(ctx context.Context) (string, time.Time, error) { return f(ctx) }
func bridgeInfo() plugin.OidcOperationInfo {
	return plugin.OidcOperationInfo{BindingID: "binding", PollInterval: 20 * time.Second, CallTimeout: 60 * time.Second, RetryDelay: 10 * time.Second, ThrottleMaxDelay: 30 * time.Second}
}
func testBridge(t *testing.T) (*authBridge, *clocktesting.FakeClock) {
	t.Helper()
	now := time.Now()
	c := clocktesting.NewFakeClock(now)
	b, err := newAuthBridgeWithClock(bridgeInfo(), "identity", now.Add(10*time.Minute), c)
	if err != nil {
		t.Fatal(err)
	}
	return b, c
}
func TestBridgeTiming(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		info    plugin.OidcOperationInfo
		timeout time.Duration
		want    time.Duration
		bad     string
	}{
		{"default", bridgeInfo(), 10 * time.Minute, 130 * time.Second, ""},
		{"custom", plugin.OidcOperationInfo{BindingID: "b", PollInterval: 90 * time.Second, CallTimeout: 45 * time.Second, RetryDelay: 5 * time.Second, ThrottleMaxDelay: 10 * time.Second}, 10 * time.Minute, 175 * time.Second, ""},
		{"missing", plugin.OidcOperationInfo{}, 10 * time.Minute, 0, "metadata"},
		{"short", bridgeInfo(), 129 * time.Second, 0, "timeoutSeconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, e := newAuthBridgeWithClock(tc.info, "auth", now.Add(tc.timeout), clocktesting.NewFakeClock(now))
			if tc.bad != "" {
				if e == nil || !strings.Contains(e.Error(), tc.bad) {
					t.Fatalf("error=%v", e)
				}
				if tc.name == "short" && !strings.Contains(e.Error(), "20s") {
					t.Fatalf("missing effective poll interval: %v", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if b.window != tc.want {
				t.Fatalf("W=%s want %s", b.window, tc.want)
			}
		})
	}
}
func TestBridgeWorkersQueueWithoutMinting(t *testing.T) {
	b, c := testBridge(t)
	var calls atomic.Int32
	src := bridgeSource(func(ctx context.Context) (string, time.Time, error) {
		calls.Add(1)
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > 30*time.Second {
			t.Error("unbounded service")
		}
		return "token", c.Now().Add(2 * time.Minute), nil
	})
	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() {
			tok, _, err := b.Token(context.Background())
			if err == nil && tok != "token" {
				err = errors.New("wrong token")
			}
			done <- err
		}()
	}
	select {
	case <-b.requests:
	case <-time.After(time.Second):
		t.Fatal("no refresh signal")
	}
	if calls.Load() != 0 {
		t.Fatal("worker minted")
	}
	if err := b.Service(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(time.Second):
			t.Fatal("worker not woken")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("refreshes not coalesced")
	}
}
func TestBridgeExpiryWaitsForLiveService(t *testing.T) {
	b, c := testBridge(t)
	if err := b.Service(context.Background(), bridgeSource(func(context.Context) (string, time.Time, error) { return "old", c.Now().Add(40 * time.Second), nil })); err != nil {
		t.Fatal(err)
	}
	c.Step(41 * time.Second)
	done := make(chan string, 1)
	go func() { tok, _, _ := b.Token(context.Background()); done <- tok }()
	select {
	case <-b.requests:
	case <-time.After(time.Second):
		t.Fatal("expired token did not request service")
	}
	select {
	case tok := <-done:
		t.Fatalf("expired token returned: %q", tok)
	default:
	}
	if err := b.Service(context.Background(), bridgeSource(func(context.Context) (string, time.Time, error) { return "new", c.Now().Add(time.Minute), nil })); err != nil {
		t.Fatal(err)
	}
	if tok := <-done; tok != "new" {
		t.Fatal(tok)
	}
}
func TestBridgeStarvationAndCancellation(t *testing.T) {
	b, c := testBridge(t)
	c.Step(131 * time.Second)
	if _, _, err := b.Token(context.Background()); !errors.Is(err, errRefreshServiceStarved) {
		t.Fatalf("gap > W: %v", err)
	}
	b, _ = testBridge(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := b.Token(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := b.Token(context.Background()); done <- err }()
	<-b.requests
	b.Close(context.Canceled)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestBridgeRepeatedTokenDoesNotExtendExpiry(t *testing.T) {
	b, c := testBridge(t)
	expiry := c.Now().Add(time.Minute)
	src := bridgeSource(func(context.Context) (string, time.Time, error) { return "same", expiry, nil })
	if err := b.Service(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	c.Step(30 * time.Second)
	if err := b.Service(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	c.Step(31 * time.Second)
	if err := b.Service(context.Background(), src); err == nil {
		t.Fatal("accepted expired repeated token")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if tok, _, _ := b.Token(ctx); tok != "" {
		t.Fatal("expired token escaped")
	}
}

func TestBridgePollingScheduleAndWaitCap(t *testing.T) {
	// A 60s handler followed by a 30s throttle gap must still fit W=130s.
	b, c := testBridge(t)
	expiry := c.Now().Add(15 * time.Minute)
	source := bridgeSource(func(context.Context) (string, time.Time, error) { return "jwt", expiry, nil })
	c.Step(60 * time.Second)
	if err := b.Service(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	c.Step(30 * time.Second)
	if tok, _, err := b.Token(context.Background()); err != nil || tok != "jwt" {
		t.Fatalf("slow handler plus throttle: %q %v", tok, err)
	}
	b.Invalidate()
	result := make(chan error, 1)
	go func() { _, _, err := b.Token(context.Background()); result <- err }()
	deadline := time.Now().Add(time.Second)
	for !c.HasWaiters() {
		if time.Now().After(deadline) {
			t.Fatal("worker failed to start timer")
		}
		time.Sleep(time.Millisecond)
	}
	c.Step(101 * time.Second)
	select {
	case err := <-result:
		if !errors.Is(err, errRefreshServiceStarved) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker exceeded W")
	}
	// A shorter remaining action deadline caps an otherwise fresh W.
	b, c = testBridge(t)
	c.Step(9 * time.Minute)
	if err := b.Service(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	b.Invalidate()
	result = make(chan error, 1)
	go func() { _, _, err := b.Token(context.Background()); result <- err }()
	deadline = time.Now().Add(time.Second)
	for !c.HasWaiters() {
		if time.Now().After(deadline) {
			t.Fatal("worker failed to start timer")
		}
		time.Sleep(time.Millisecond)
	}
	c.Step(time.Minute)
	select {
	case err := <-result:
		if !errors.Is(err, errRefreshServiceStarved) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker exceeded action deadline")
	}
}

func TestBridgeServiceRespectsRemainingCallbackAndFencesInvalidation(t *testing.T) {
	b, c := testBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := b.Service(ctx, bridgeSource(func(ctx context.Context) (string, time.Time, error) { <-ctx.Done(); return "", time.Time{}, ctx.Err() }))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("unbounded service: %v", err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- b.Service(context.Background(), bridgeSource(func(context.Context) (string, time.Time, error) {
			close(entered)
			<-release
			return "rejected", c.Now().Add(time.Minute), nil
		}))
	}()
	<-entered
	b.Invalidate()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	result := make(chan string, 1)
	wait, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	go func() { tok, _, _ := b.Token(wait); result <- tok }()
	select {
	case tok := <-result:
		t.Fatalf("invalidated result resurrected: %q", tok)
	case <-time.After(10 * time.Millisecond):
	}
	if err := b.Service(context.Background(), bridgeSource(func(context.Context) (string, time.Time, error) { return "fresh", c.Now().Add(time.Minute), nil })); err != nil {
		t.Fatal(err)
	}
	if tok := <-result; tok != "fresh" {
		t.Fatal(tok)
	}
}

func TestBridgeServiceBudgetIncludesQueueWait(t *testing.T) {
	b, c := testBridge(t)
	entered, release := make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- b.Service(context.Background(), bridgeSource(func(context.Context) (string, time.Time, error) {
			close(entered)
			<-release
			return "first", c.Now().Add(time.Minute), nil
		}))
	}()
	<-entered
	// Explicitly occupy the service slot, then release it after the second call
	// has spent a measurable part of its 30s allowance waiting for that slot.
	timer := time.AfterFunc(50*time.Millisecond, func() { close(release) })
	defer timer.Stop()
	var remaining time.Duration
	err := b.Service(context.Background(), bridgeSource(func(ctx context.Context) (string, time.Time, error) {
		d, _ := ctx.Deadline()
		remaining = time.Until(d)
		return "second", c.Now().Add(time.Minute), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if remaining > 29980*time.Millisecond {
		t.Fatalf("service renewed budget after queue wait: %s", remaining)
	}
}
