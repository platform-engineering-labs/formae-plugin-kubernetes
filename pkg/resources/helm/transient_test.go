//go:build unit

package helm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae/pkg/credential"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestTransientUIDStatusDoesNotTouchUnidentifiedFlight(t *testing.T) {
	clearFlights()
	defer clearFlights()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 500) }))
	defer server.Close()
	cfg := fixtureKubeconfig(t, server)
	r := testReleaseForConfig(t, cfg)
	b, _ := testBridge(t)
	identity, _, _ := flightIdentity(context.Background(), cfg)
	f := inflight{authFingerprint: identity, op: opInstall, revision: 1, generation: uuid.NewString(), bridge: b, deadline: time.Now().Add(time.Hour)}
	reserveFlight("UID|unidentified", "ns", "release", f)
	before := b.serviced
	id := flightRequestID("ns", "release", &f)
	got, err := r.Status(context.Background(), &resource.StatusRequest{RequestID: id})
	if err != nil || got.ProgressResult.OperationStatus != resource.OperationStatusInProgress || got.ProgressResult.RequestID != id {
		t.Fatalf("lost progress: %v %v", got, err)
	}
	after := lookupFlight("UID|unidentified", "ns", "release")
	if after == nil || after.statusReaders != 0 || after.reported || !b.serviced.Equal(before) || b.token != "" {
		t.Fatal("unidentified Status touched flight")
	}
}
func TestAfterStartTransientErrorsPreserveFlightAndTerminalOutcomes(t *testing.T) {
	for _, operation := range []opKind{opInstall, opUpgrade, opDelete} {
		for _, mode := range []string{"transient", "protocol", "callback-budget", "permanent", "finished", "deadline", "starved"} {
			t.Run(string(operation)+"/"+mode, func(t *testing.T) {
				clearFlights()
				defer clearFlights()
				f := inflight{op: operation, revision: 1, generation: uuid.NewString(), deadline: time.Now().Add(time.Hour)}
				err := error(credential.ErrMintFailed)
				switch mode {
				case "protocol":
					err = credential.ErrInternal
				case "callback-budget":
					err = context.DeadlineExceeded
				case "permanent":
					err = credential.ErrInvalidAudience
				case "finished":
					f.finished = true
					f.outcome = errors.New("worker failure")
				case "deadline":
					f.deadline = time.Now().Add(-time.Second)
				case "starved":
					err = errRefreshServiceStarved
				}
				reserveFlight("UID|test", "ns", "name", f)
				got, reported := progressAfterFlightError("UID|test", "ns", "name", &f, operation == opInstall, auth.RedactError(err))
				switch mode {
				case "transient", "protocol", "callback-budget":
					if reported != nil || got == nil || got.OperationStatus != resource.OperationStatusInProgress || got.RequestID != flightRequestID("ns", "name", &f) {
						t.Fatalf("lost flight: %v %v", got, reported)
					}
				case "deadline":
					if reported != nil || got.OperationStatus != resource.OperationStatusFailure {
						t.Fatal("deadline hidden")
					}
				case "finished":
					if !errors.Is(reported, f.outcome) {
						t.Fatal("worker failure hidden")
					}
				default:
					if reported == nil {
						t.Fatal("permanent failure hidden")
					}
				}
			})
		}
	}
}
func TestBridge401InvalidatesSharedCacheOnlyOnCallback(t *testing.T) {
	b, _ := testBridge(t)
	calls := 0
	source := auth.NewCachedTokenSource(bridgeSource(func(context.Context) (string, time.Time, error) {
		calls++
		return string(rune('a' + calls)), time.Now().Add(time.Hour), nil
	}))
	if err := b.Service(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	old, _, _ := b.Token(context.Background())
	b.Invalidate()
	if calls != 1 {
		t.Fatal("bridge invalidation minted without callback")
	}
	if err := b.Service(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	fresh, _, _ := b.Token(context.Background())
	if old == fresh || calls != 2 {
		t.Fatal("bridge 401 reused rejected token")
	}
	if err := b.Service(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("repeated callback unnecessarily exchanged token")
	}
}

func TestUnidentifiedFlightObserverIsBoundedAndReadOnly(t *testing.T) {
	for _, mode := range []string{"live", "finished-failure", "finished-success", "deadline", "generation", "auth", "binding", "namespace", "revision", "operation", "missing"} {
		t.Run(mode, func(t *testing.T) {
			clearFlights()
			defer clearFlights()
			b, _ := testBridge(t)
			now := time.Now()
			f := inflight{op: opInstall, revision: 1, generation: uuid.NewString(), authFingerprint: "auth", bindingID: "binding", bridge: b, deadline: now.Add(time.Minute)}
			switch mode {
			case "finished-failure":
				f.finished = true
				f.outcome = errors.New("worker failed")
			case "finished-success":
				f.finished = true
			case "deadline":
				f.deadline = now.Add(-time.Second)
			}
			reserveFlight("UID|hidden", "ns", "release", f)
			gen, identity, binding, ns, rev, op := f.generation, "auth", "binding", "ns", 1, opInstall
			switch mode {
			case "generation":
				gen = uuid.NewString()
			case "auth":
				identity = "other"
			case "binding":
				binding = "other"
			case "namespace":
				ns = "other"
			case "revision":
				rev = 2
			case "operation":
				op = opDelete
			case "missing":
				clearFlights()
			}
			before := lookupFlight("UID|hidden", "ns", "release")
			servicedBefore := b.serviced
			got := observeUnidentifiedFlight(ns, "release", rev, op, gen, identity, binding, "opaque")
			want := resource.OperationStatusFailure
			if mode == "live" || mode == "finished-success" {
				want = resource.OperationStatusInProgress
			}
			if got.ProgressResult.OperationStatus != want {
				t.Fatalf("%s: %+v", mode, got.ProgressResult)
			}
			after := lookupFlight("UID|hidden", "ns", "release")
			if before != nil && (after == nil || after.reported || after.statusReaders != 0 || after.generation != before.generation || !after.bridge.serviced.Equal(servicedBefore) || after.bridge.token != "") {
				t.Fatal("observer mutated flight")
			}
		})
	}
}

func TestOpaqueBrokerFailureCannotExtendOrFeedRetainedFlight(t *testing.T) {
	clearFlights()
	defer clearFlights()
	b, clock := testBridge(t)
	originalService := b.serviced
	f := inflight{op: opInstall, revision: 1, generation: uuid.NewString(), authFingerprint: "auth", bindingID: "binding", bridge: b, deadline: time.Now().Add(time.Minute)}
	reserveFlight("UID|opaque", "ns", "name", f)
	for i := 0; i < 3; i++ {
		_, err := serviceFlight(context.Background(), "UID|opaque", "ns", "name", &f, bridgeSource(func(context.Context) (string, time.Time, error) { return "", time.Time{}, credential.ErrInternal }))
		if !isFlightServiceError(err) {
			t.Fatal("bounded receiver timeout was lost")
		}
		p, err := progressAfterFlightError("UID|opaque", "ns", "name", &f, true, err)
		if err != nil || p.OperationStatus != resource.OperationStatusInProgress {
			t.Fatalf("opaque failure prematurely terminal: %v %v", p, err)
		}
		if !b.serviced.Equal(originalService) || b.token != "" {
			t.Fatal("failed service fed or extended bridge")
		}
	}
	clock.Step(b.window + time.Second)
	if _, _, err := b.Token(context.Background()); !errors.Is(err, errRefreshServiceStarved) {
		t.Fatal("opaque service failure extended starvation boundary")
	}
	updateOwnedFlight("UID|opaque", "ns", "name", f.generation, func(f *inflight) { f.deadline = time.Now().Add(-time.Second) })
	got := observeUnidentifiedFlight("ns", "name", 1, opInstall, f.generation, "auth", "binding", flightRequestID("ns", "name", &f))
	if got.ProgressResult.OperationStatus != resource.OperationStatusFailure {
		t.Fatal("opaque failures permit endless progress")
	}
	if auth.IsTransient(credential.ErrInternal) {
		t.Fatal("protocol faults became globally retryable")
	}
}

func TestAfterStartReturnedWorkerFailureIsConsumed(t *testing.T) {
	for _, op := range []opKind{opInstall, opUpgrade, opDelete} {
		for _, readerHeld := range []bool{false, true} {
			for _, sameError := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/reader=%t/same-error=%t", op, readerHeld, sameError), func(t *testing.T) {
					clearFlights()
					defer clearFlights()
					const scope, ns, name = "UID|delivered", "ns", "name"
					outcome := errors.New("worker failed")
					f := inflight{op: op, requestOp: op, revision: 1, generation: uuid.NewString(), fingerprint: "original"}
					reserveFlight(scope, ns, name, f)
					if readerHeld {
						if own, reserved := reserveRejoinFlight(scope, ns, name, f); reserved || own == nil || own.statusReaders != 1 {
							t.Fatal("rejoin did not retain original generation")
						}
					}
					finishFlight(scope, ns, name, f.generation, outcome)
					observed := error(context.DeadlineExceeded)
					if sameError {
						observed = outcome
					}
					_, reported := progressAfterFlightError(scope, ns, name, &f, op == opInstall, observed)
					if !errors.Is(reported, outcome) {
						t.Fatal("worker outcome was not returned")
					}
					retained := lookupFlight(scope, ns, name)
					if readerHeld {
						if retained == nil || !retained.reported || retained.statusReaders != 1 {
							t.Fatal("returned outcome must be marked reported while the reader retains its generation")
						}
						if _, reserved := reserveFlight(scope, ns, name, inflight{generation: "competing"}); reserved {
							t.Fatal("terminal delivery removed a generation still held by a reader")
						}
						releaseStatusFlight(scope, ns, name, f.generation)
					} else if retained != nil {
						t.Fatal("returned worker error left an unreported generation")
					}
					corrected := f
					corrected.generation = uuid.NewString()
					corrected.fingerprint = "corrected"
					if _, reserved := reserveFlight(scope, ns, name, corrected); !reserved {
						t.Fatal("delivered failure blocks corrected operation until retention cleanup")
					}
					// A late callback for the old generation cannot consume its successor.
					finishFlight(scope, ns, name, corrected.generation, outcome)
					p, err := progressAfterFlightError(scope, ns, name, &f, op == opInstall, observed)
					if err != nil || p.ErrorCode != resource.OperationErrorCodeResourceConflict {
						t.Fatal("late callback did not reject the successor generation")
					}
					if successor := lookupFlight(scope, ns, name); successor == nil || successor.reported || successor.generation != corrected.generation {
						t.Fatal("late callback consumed the successor outcome")
					}
				})
			}
		}
	}
}
