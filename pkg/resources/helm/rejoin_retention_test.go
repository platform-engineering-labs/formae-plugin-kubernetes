//go:build unit

package helm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// Removing a terminal Status reader must not discard the immutable outcome
// still being observed by a matching Create, Update or Delete rejoin.
func TestRejoinRetainsOutcomeAcrossConcurrentTerminalStatus(t *testing.T) {
	for _, op := range []opKind{opInstall, opUpgrade, opDelete} {
		for _, workerErr := range []error{nil, errors.New("worker failed")} {
			t.Run(string(op)+"/"+errorLabel(workerErr), func(t *testing.T) {
				clearFlights()
				defer clearFlights()
				const scope, ns, name = "UID|rejoin-retention", "ns", "release"
				bridge, clock := testBridge(t)
				f := inflight{op: op, requestOp: op, revision: 1, generation: uuid.NewString(), fingerprint: "desired", authFingerprint: "identity", bindingID: "binding", bridge: bridge}
				if _, reserved := reserveFlight(scope, ns, name, f); !reserved {
					t.Fatal("initial reservation")
				}
				wanted := f
				wanted.generation = uuid.NewString()
				wanted.preparing = true
				own, reserved := reserveRejoinFlight(scope, ns, name, wanted)
				if reserved || own == nil || own.generation != f.generation {
					t.Fatal("matching rejoin did not acquire original generation")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				entered, unblock := make(chan struct{}), make(chan struct{})
				type result struct {
					flight *inflight
					err    error
				}
				done := make(chan result, 1)
				go func() {
					fresh, err := serviceFlight(ctx, scope, ns, name, own, bridgeSource(func(ctx context.Context) (string, time.Time, error) {
						close(entered)
						select {
						case <-unblock:
							return "token", clock.Now().Add(time.Minute), nil
						case <-ctx.Done():
							return "", time.Time{}, ctx.Err()
						}
					}))
					done <- result{fresh, err}
				}()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("service never entered")
				}
				finishFlight(scope, ns, name, f.generation, workerErr)
				status, recovery := acquireStatusFlight(scope, ns, name, f.generation, f)
				if recovery || status == nil {
					t.Fatal("matching Status lost original flight")
				}
				reconcileFlightResult(scope, ns, name, f.generation, flightRequestID(ns, name, &f), &resource.StatusResult{ProgressResult: &resource.ProgressResult{OperationStatus: resource.OperationStatusSuccess}})
				releaseStatusFlight(scope, ns, name, f.generation)
				// Even retention cleanup or a competing apply cannot release the held
				// generation while rejoin service is still using it.
				removeOwnedFlight(scope, ns, name, f.generation)
				if _, reserved := reserveFlight(scope, ns, name, inflight{generation: "competing"}); reserved {
					t.Error("terminal Status released ownership before rejoin returned")
				}
				close(unblock)
				select {
				case got := <-done:
					if got.err != nil {
						t.Fatalf("worker outcome became rejoin error: %v", got.err)
					}
					if got.flight == nil || got.flight.generation != f.generation || !got.flight.finished || got.flight.outcome != workerErr {
						t.Fatalf("rejoin lost immutable outcome: %#v", got.flight)
					}
				case <-ctx.Done():
					t.Fatal("service did not return")
				}
				releaseStatusFlight(scope, ns, name, f.generation)
				if lookupFlight(scope, ns, name) != nil {
					t.Fatal("reported flight remained after rejoin callback exit")
				}
			})
		}
	}
}

func errorLabel(err error) string {
	if err == nil {
		return "success"
	}
	return "failure"
}

func TestRejoinDoesNotRetainForeignOrPreparingFlight(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*inflight)
	}{
		{"desired", func(f *inflight) { f.fingerprint = "other" }},
		{"request operation", func(f *inflight) { f.requestOp = opUpgrade }},
		{"auth", func(f *inflight) { f.authFingerprint = "other" }},
		{"binding", func(f *inflight) { f.bindingID = "other" }},
		{"preparing", func(f *inflight) { f.preparing = true }},
		{"delete operation", func(f *inflight) { f.op = opDelete }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearFlights()
			defer clearFlights()
			wanted := inflight{op: opInstall, requestOp: opInstall, generation: "original", fingerprint: "desired", authFingerprint: "auth", bindingID: "binding"}
			original := wanted
			tc.mutate(&original)
			if tc.name == "delete operation" {
				wanted.op = opDelete
				original.op = opInstall
			}
			reserveFlight("UID|foreign", "ns", "release", original)
			got, reserved := reserveRejoinFlight("UID|foreign", "ns", "release", wanted)
			if reserved || got == nil || matchesRejoin(got, &wanted) {
				t.Fatal("foreign flight joined")
			}
			removeOwnedFlight("UID|foreign", "ns", "release", original.generation)
			if lookupFlight("UID|foreign", "ns", "release") != nil {
				t.Fatal("foreign rejoin pinned another flight")
			}
		})
	}
}
