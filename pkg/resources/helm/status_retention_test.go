//go:build unit

package helm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestStatusTransientRecoveryRetainsReaderUntilOutcomeObserved(t *testing.T) {
	for _, workerFails := range []bool{false, true} {
		t.Run(fmt.Sprint(workerFails), func(t *testing.T) {
			clearFlights()
			defer clearFlights()
			entered, release := make(chan struct{}), make(chan struct{})
			var reads atomic.Int32
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			cfg := flightFixture(t, "status-retention", func(w http.ResponseWriter, r *http.Request) {
				if reads.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					http.Error(w, "transient storage outage", http.StatusInternalServerError)
					return
				}
				emptySecrets(w, r)
			})
			defer unblock()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r := testReleaseForConfig(t, cfg)
			scope, err := resolveFlightScope(ctx, r.Client)
			if err != nil {
				t.Fatal(err)
			}
			identity, _, err := flightIdentity(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			f := inflight{op: opDelete, revision: 1, generation: uuid.NewString(), authFingerprint: identity, deadline: time.Now().Add(time.Minute)}
			if _, ok := reserveFlight(scope, "ns", "release", f); !ok {
				t.Fatal("reserve flight")
			}
			req := &resource.StatusRequest{RequestID: flightRequestID("ns", "release", &f)}
			type response struct {
				result *resource.StatusResult
				err    error
			}
			late := make(chan response, 1)
			go func() {
				result, err := r.Status(ctx, req)
				late <- response{result, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first Status did not reach storage")
			}
			var outcome error
			if workerFails {
				outcome = errors.New("retained worker failure")
			}
			finishFlight(scope, "ns", "release", f.generation, outcome)
			first, err := r.Status(ctx, req)
			want := resource.OperationStatusSuccess
			if workerFails {
				want = resource.OperationStatusFailure
			}
			if err != nil || first.ProgressResult.OperationStatus != want {
				t.Fatalf("concurrent terminal Status: %v %v", first, err)
			}
			retained := lookupFlight(scope, "ns", "release")
			if retained == nil || !retained.reported || retained.statusReaders != 1 {
				t.Fatal("terminal Status did not retain the blocked reader")
			}
			unblock()
			var got response
			select {
			case got = <-late:
			case <-ctx.Done():
				t.Fatal("blocked Status did not return")
			}
			if got.err != nil || got.result == nil || got.result.ProgressResult == nil {
				t.Fatalf("late Status: %v %v", got.result, got.err)
			}
			p := got.result.ProgressResult
			if workerFails {
				if p.OperationStatus != resource.OperationStatusFailure || p.ErrorCode == resource.OperationErrorCodeResourceConflict || !strings.Contains(p.StatusMessage, outcome.Error()) {
					t.Fatalf("late Status lost retained worker outcome: %+v", p)
				}
			} else if p.OperationStatus != resource.OperationStatusInProgress || p.RequestID != req.RequestID {
				t.Fatalf("transient read after concurrent success became terminal: %+v", p)
			}
			if lookupFlight(scope, "ns", "release") != nil {
				t.Fatal("last reader stranded the reported generation")
			}
			if !workerFails {
				final, err := r.Status(ctx, req)
				if err != nil || final.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
					t.Fatalf("retry did not confirm completed uninstall: %v %v", final, err)
				}
			}
		})
	}
}
