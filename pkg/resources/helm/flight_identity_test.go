//go:build unit

package helm

import (
	"context"
	"errors"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"strings"
	"testing"
	"time"
)

func TestFlightReservationCannotOverwriteOrRemoveAnotherOwner(t *testing.T) {
	clearFlights()
	defer clearFlights()
	cfg := testTarget(t, "same")
	first, ok := reserveFlight(testScope(cfg), "ns", "release", inflight{fingerprint: "desired", authFingerprint: "first", generation: "one", deadline: time.Now().Add(time.Minute)})
	if !ok {
		t.Fatal("first reservation failed")
	}
	second, ok := reserveFlight(testScope(cfg), "ns", "release", inflight{authFingerprint: "second", generation: "two"})
	if ok || second.generation != "one" {
		t.Fatal("competing identity replaced owner")
	}
	removeOwnedFlight(testScope(cfg), "ns", "release", "two")
	if lookupFlight(testScope(cfg), "ns", "release") == nil {
		t.Fatal("foreign generation removed owner")
	}
	removeOwnedFlight(testScope(cfg), "ns", "release", first.generation)
	if lookupFlight(testScope(cfg), "ns", "release") != nil {
		t.Fatal("owner could not remove flight")
	}
}

func TestRequestIDGenerationAndLegacyCompatibility(t *testing.T) {
	for _, id := range []string{"ns/release@1:install", "ns/release@2:upgrade#39c24d1d-3815-4817-9242-4032be46601b"} {
		if _, _, _, _, err := parseRequestID(id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	for _, id := range []string{"ns/release@1:install#", "ns/release@1:install#secret", "ns/release@0:install", "ns/release@1:unknown"} {
		if _, _, _, _, err := parseRequestID(id); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
}

func TestFlightPhysicalKeySeparatesAuthorization(t *testing.T) {
	clearFlights()
	defer clearFlights()
	first, _ := config.FromTargetConfig([]byte(`{"Auth":{"Type":"EKS","Endpoint":"https://physical.example","CertificateAuthority":"Y2E=","ClusterName":"c","Region":"r","Profile":"a"}}`))
	second, _ := config.FromTargetConfig([]byte(`{"Auth":{"Type":"EKS","Endpoint":"https://physical.example:443/","CertificateAuthority":"Y2E=","ClusterName":"c","Region":"r","Profile":"b"}}`))
	identity, _ := first.AuthFingerprint()
	other, _ := second.AuthFingerprint()
	if identity == other {
		t.Fatal("test identities unexpectedly equal")
	}
	_, ok := reserveFlight(testScope(first), "ns", "release", inflight{authFingerprint: identity, generation: "owner"})
	if !ok {
		t.Fatal("reservation failed")
	}
	f, ok := reserveFlight(testScope(second), "ns", "release", inflight{authFingerprint: other, generation: "other"})
	if ok || f.authFingerprint != identity {
		t.Fatal("same endpoint with different identity replaced physical flight")
	}
}

func TestPreRecordStatusAndTerminalOutcome(t *testing.T) {
	clearFlights()
	defer clearFlights()
	cfg := flightFixture(t, "uid", emptySecrets)
	scope, err := resolveTestFlightScope(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := testRelease(t, cfg)
	f := inflight{op: opInstall, revision: 1, generation: "39c24d1d-3815-4817-9242-4032be46601b", deadline: time.Now().Add(time.Minute)}
	identity, _, err := flightIdentity(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.authFingerprint = identity
	reserveFlight(scope, "ns", "release", f)
	result, err := r.installStatus(context.Background(), nil, nil, &f, "ns", "release", 1, opInstall, flightRequestID("ns", "release", &f))
	if err != nil || result.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("slow pre-record install: %v %v", result, err)
	}
	finishFlight(scope, "ns", "release", f.generation, errors.New("template failed before recording"))
	// Mismatched poll cannot consume the immutable terminal result.
	mismatch, err := r.Status(context.Background(), &resource.StatusRequest{RequestID: requestID("ns", "release", 1, opInstall)})
	if err != nil || mismatch.ProgressResult.ErrorCode != resource.OperationErrorCodeResourceConflict {
		t.Fatalf("mismatch: %v %v", mismatch, err)
	}
	if lookupFlight(scope, "ns", "release") == nil {
		t.Fatal("mismatch consumed result")
	}
	result, err = r.Status(context.Background(), &resource.StatusRequest{RequestID: flightRequestID("ns", "release", &f)})
	if err != nil || result.ProgressResult.OperationStatus != resource.OperationStatusFailure || !strings.Contains(result.ProgressResult.StatusMessage, "template failed") {
		t.Fatalf("terminal error lost: %v %v", result, err)
	}
	if lookupFlight(scope, "ns", "release") != nil {
		t.Fatal("matching poll did not consume terminal result")
	}
}

func TestFlightTerminalOutcomeIsImmutable(t *testing.T) {
	clearFlights()
	defer clearFlights()
	cfg := testTarget(t, "same")
	reserveFlight(testScope(cfg), "ns", "release", inflight{generation: "owner"})
	first := errors.New("first outcome")
	finishFlight(testScope(cfg), "ns", "release", "owner", first)
	finishFlight(testScope(cfg), "ns", "release", "owner", errors.New("late outcome"))
	if lookupFlight(testScope(cfg), "ns", "release").outcome != first {
		t.Fatal("terminal outcome was overwritten")
	}
}
