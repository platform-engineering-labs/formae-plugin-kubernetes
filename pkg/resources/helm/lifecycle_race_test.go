//go:build unit

package helm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// Every server has independent selector/TLS data; equal namespace UID models
// aliases for one physical API server without trusting DNS or its certificate.
func flightFixture(t *testing.T, uid string, handler http.HandlerFunc) *config.Config {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/namespaces/kube-system" {
			fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"kube-system","uid":%q}}`, uid)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return fixtureKubeconfig(t, server)
}
func fixtureKubeconfig(t *testing.T, server *httptest.Server) *config.Config {
	t.Helper()
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	path := filepath.Join(t.TempDir(), "config")
	data := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: %s\n    certificate-authority-data: %s\ncontexts:\n- name: test\n  context:\n    cluster: test\n    user: test\ncurrent-context: test\nusers:\n- name: test\n  user: {}\n", server.URL, ca)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"Auth": map[string]string{"Type": "Kubeconfig", "Kubeconfig": path}})
	cfg, err := config.FromTargetConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
func emptySecrets(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, `{"apiVersion":"v1","kind":"SecretList","items":[]}`)
}

func TestClusterScopeUsesLiveUIDAcrossSelectors(t *testing.T) {
	clearFlights()
	defer clearFlights()
	a := flightFixture(t, "same-uid", emptySecrets)
	b := flightFixture(t, "same-uid", emptySecrets)
	other := flightFixture(t, "other-uid", emptySecrets)
	sa, err := resolveTestFlightScope(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := resolveTestFlightScope(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := resolveTestFlightScope(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if sa != sb || sa == sc {
		t.Fatalf("UID scopes: %q %q %q", sa, sb, sc)
	}
	if _, ok := reserveFlight(sa, "ns", "release", inflight{generation: "a"}); !ok {
		t.Fatal("reserve a")
	}
	if _, ok := reserveFlight(sb, "ns", "release", inflight{generation: "b"}); ok {
		t.Fatal("alias bypassed physical reservation")
	}
	if _, ok := reserveFlight(sc, "ns", "release", inflight{generation: "c"}); !ok {
		t.Fatal("independent cluster blocked")
	}
}
func TestClusterIdentityDenialPreventsHelmMutation(t *testing.T) {
	var mutations atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations.Add(1)
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	cfg := fixtureKubeconfig(t, server)
	r := testReleaseForConfig(t, cfg)
	_, err := r.Create(context.Background(), &resource.CreateRequest{Properties: []byte(`{"metadata":{"name":"release","namespace":"ns"},"chart":"./missing"}`)})
	if err == nil || !strings.Contains(err.Error(), "kube-system") {
		t.Fatalf("identity denial not actionable: %v", err)
	}
	if mutations.Load() != 0 {
		t.Fatal("identity denial mutated Helm state")
	}
}
func TestStatusTerminalRecordWaitsForWorkerOutcome(t *testing.T) {
	for _, finishBeforeRead := range []bool{false, true} {
		t.Run(fmt.Sprint(finishBeforeRead), func(t *testing.T) {
			clearFlights()
			defer clearFlights()
			entered, release := make(chan struct{}), make(chan struct{})
			var reads atomic.Int32
			cfg := flightFixture(t, "uid", func(w http.ResponseWriter, r *http.Request) {
				if reads.Add(1) == 1 {
					close(entered)
					<-release
				}
				emptySecrets(w, r)
			})
			scope, err := resolveTestFlightScope(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			identity, _, _ := flightIdentity(context.Background(), cfg)
			f := inflight{op: opDelete, revision: 1, generation: uuid.NewString(), authFingerprint: identity, deadline: time.Now().Add(time.Minute)}
			reserveFlight(scope, "ns", "release", f)
			r := testReleaseForConfig(t, cfg)
			req := &resource.StatusRequest{RequestID: flightRequestID("ns", "release", &f)}
			results := make(chan *resource.StatusResult, 1)
			errs := make(chan error, 1)
			go func() { got, err := r.Status(context.Background(), req); results <- got; errs <- err }()
			<-entered
			if finishBeforeRead {
				finishFlight(scope, "ns", "release", f.generation, nil)
			}
			close(release)
			got := <-results
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
			if !finishBeforeRead {
				if got.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
					t.Fatal("reported terminal record before worker outcome")
				}
				finishFlight(scope, "ns", "release", f.generation, nil)
				got, err = r.Status(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
			}
			if got.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
				t.Fatalf("terminal=%v", got.ProgressResult)
			}
			if lookupFlight(scope, "ns", "release") != nil {
				t.Fatal("successful report stranded flight")
			}
		})
	}
}

func TestStatusRecoveryReservesBeforeStorageInspection(t *testing.T) {
	clearFlights()
	defer clearFlights()
	entered, release := make(chan struct{}), make(chan struct{})
	cfg := flightFixture(t, "uid", func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; emptySecrets(w, r) })
	scope, err := resolveTestFlightScope(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := (testReleaseForConfig(t, cfg)).Status(context.Background(), &resource.StatusRequest{RequestID: "ns/release@1:install"})
		done <- err
	}()
	<-entered
	_, reserved := reserveFlight(scope, "ns", "release", inflight{generation: "competing-worker"})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reserved {
		t.Fatal("competing worker started inside Status recovery read/mutation window")
	}
}

func TestSynchronousTemplateFailureDoesNotBlockCorrectedApply(t *testing.T) {
	clearFlights()
	defer clearFlights()
	cfg := flightFixture(t, "uid", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"major":"1","minor":"36","gitVersion":"v1.36.1"}`)
		case "/api":
			fmt.Fprint(w, `{"kind":"APIVersions","versions":["v1"]}`)
		case "/apis":
			fmt.Fprint(w, `{"kind":"APIGroupList","groups":[]}`)
		case "/api/v1":
			fmt.Fprint(w, `{"kind":"APIResourceList","groupVersion":"v1","resources":[{"name":"secrets","namespaced":true,"kind":"Secret","verbs":["get","list","create"]}]}`)
		default:
			emptySecrets(w, r)
		}
	})
	chart := t.TempDir()
	if err := os.Mkdir(filepath.Join(chart, "templates"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte("apiVersion: v2\nname: broken\nversion: 0.1.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "templates", "fail.yaml"), []byte(`{{ fail .Values.message }}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := testReleaseForConfig(t, cfg)
	for _, message := range []string{"original-template-error", "corrected-worker-ran"} {
		raw, _ := json.Marshal(map[string]any{"metadata": map[string]string{"name": "release", "namespace": "ns"}, "chart": chart, "values": map[string]string{"message": message}})
		result, err := r.Create(context.Background(), &resource.CreateRequest{Properties: raw})
		if err == nil || !strings.Contains(err.Error(), message) {
			t.Fatalf("desired %s did not run its worker: %v %v", message, result, err)
		}
	}
}

func TestDrainRejectsPreparedWorkerAfterInitialScan(t *testing.T) {
	clearFlights()
	defer clearFlights()
	entered := make(chan struct{})
	scope := "UID|drain"
	reserveFlight(scope, "ns", "release", inflight{generation: "owner", preparing: true, cancel: func() { close(entered) }})
	drained := make(chan bool, 1)
	go func() { drained <- DrainInFlight(time.Second) }()
	<-entered
	_, started := activateFlight(scope, "ns", "release", "owner", func() {}, func(*inflight) {})
	removeOwnedFlight(scope, "ns", "release", "owner")
	if !<-drained {
		t.Fatal("drain failed to finish after preparation unwound")
	}
	if started {
		t.Fatal("preparing callback published a detached worker after drain scanned it")
	}
}

func TestWorkerCompletionDuringCredentialServiceIsNotCallbackFailure(t *testing.T) {
	clearFlights()
	defer clearFlights()
	b, c := testBridge(t)
	scope := "UID|service"
	f := inflight{generation: uuid.NewString(), bridge: b}
	reserveFlight(scope, "ns", "release", f)
	entered, release := make(chan struct{}), make(chan struct{})
	results := make(chan *inflight, 1)
	errs := make(chan error, 1)
	go func() {
		fresh, err := serviceFlight(context.Background(), scope, "ns", "release", &f, bridgeSource(func(context.Context) (string, time.Time, error) {
			close(entered)
			<-release
			return "token", c.Now().Add(time.Minute), nil
		}))
		results <- fresh
		errs <- err
	}()
	<-entered
	finishFlight(scope, "ns", "release", f.generation, nil)
	close(release)
	fresh := <-results
	if err := <-errs; err != nil {
		t.Fatalf("successful completion became callback error: %v", err)
	}
	if fresh == nil || !fresh.finished || fresh.outcome != nil {
		t.Fatal("service did not reconcile worker outcome")
	}
}

func TestConcurrentStatusCannotReleaseRecoveryOwnershipEarly(t *testing.T) {
	clearFlights()
	defer clearFlights()
	entered, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	cfg := flightFixture(t, "uid", func(w http.ResponseWriter, r *http.Request) {
		if reads.Add(1) == 1 {
			close(entered)
			<-release
		}
		emptySecrets(w, r)
	})
	scope, err := resolveTestFlightScope(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	identity, _, _ := flightIdentity(context.Background(), cfg)
	f := inflight{op: opDelete, revision: 1, generation: uuid.NewString(), authFingerprint: identity, finished: true}
	reserveFlight(scope, "ns", "release", f)
	r := testReleaseForConfig(t, cfg)
	req := &resource.StatusRequest{RequestID: flightRequestID("ns", "release", &f)}
	done := make(chan error, 1)
	go func() { _, err := r.Status(context.Background(), req); done <- err }()
	<-entered
	second, err := r.Status(context.Background(), req)
	if err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	if second == nil || second.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		close(release)
		<-done
		t.Fatal("second Status failed")
	}
	_, reserved := reserveFlight(scope, "ns", "release", inflight{generation: "new-worker"})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reserved {
		t.Fatal("one terminal poll released ownership while another still read/recovered")
	}
	if lookupFlight(scope, "ns", "release") != nil {
		t.Fatal("last terminal reader did not release consumed outcome")
	}
}

func TestRetentionCleanupWaitsForStatusReader(t *testing.T) {
	clearFlights()
	defer clearFlights()
	entered, release := make(chan struct{}), make(chan struct{})
	cfg := flightFixture(t, "uid", func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; emptySecrets(w, r) })
	scope, err := resolveTestFlightScope(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	identity, _, _ := flightIdentity(context.Background(), cfg)
	f := inflight{op: opDelete, revision: 1, generation: uuid.NewString(), authFingerprint: identity, finished: true}
	reserveFlight(scope, "ns", "release", f)
	done := make(chan error, 1)
	go func() {
		_, err := (testReleaseForConfig(t, cfg)).Status(context.Background(), &resource.StatusRequest{RequestID: flightRequestID("ns", "release", &f)})
		done <- err
	}()
	<-entered
	// The retention timer calls exactly this generation-fenced removal path.
	removeOwnedFlight(scope, "ns", "release", f.generation)
	_, reserved := reserveFlight(scope, "ns", "release", inflight{generation: "new"})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reserved {
		t.Fatal("cleanup timer released ownership during active recovery reader")
	}
	if lookupFlight(scope, "ns", "release") != nil {
		t.Fatal("expired flight survived last reader")
	}
}

func TestInventoryInvalidationFencesEarlierBuild(t *testing.T) {
	invMu.Lock()
	previous := invCache
	invCache = map[string]inventoryEntry{}
	invMu.Unlock()
	defer func() { invMu.Lock(); invCache = previous; invMu.Unlock() }()
	entered, release := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	cfg := flightFixture(t, "uid", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			fmt.Fprint(w, `{"gitVersion":"v1.36.1"}`)
			return
		}
		if r.URL.Path != "/api/v1/secrets" {
			t.Errorf("unexpected inventory path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		emptySecrets(w, r)
	})
	done := make(chan error, 1)
	go func() { _, err := cachedInventory(context.Background(), cfg); done <- err }()
	<-entered
	invalidateInventory(cfg)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	invMu.Lock()
	count := len(invCache)
	invMu.Unlock()
	if count != 0 {
		t.Fatal("pre-mutation inventory build republished after invalidation")
	}
}

func TestStatusDeadlineFailureRetainsUnfinishedFlight(t *testing.T) {
	for _, op := range []opKind{opInstall, opDelete} {
		t.Run(string(op), func(t *testing.T) {
			clearFlights()
			defer clearFlights()
			var storageReads, cancellations atomic.Int32
			cfg := flightFixture(t, "deadline-uid", func(w http.ResponseWriter, r *http.Request) {
				storageReads.Add(1)
				emptySecrets(w, r)
			})
			scope, err := resolveTestFlightScope(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			identity, _, err := flightIdentity(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			f := inflight{op: op, revision: 1, generation: uuid.NewString(), authFingerprint: identity,
				deadline: time.Now().Add(-time.Second), cancel: func() { cancellations.Add(1) }}
			if _, ok := reserveFlight(scope, "ns", "release", f); !ok {
				t.Fatal("reserve unfinished worker")
			}
			r := testReleaseForConfig(t, cfg)
			req := &resource.StatusRequest{RequestID: flightRequestID("ns", "release", &f)}
			// Cancellation deliberately does not complete the worker. Repeated polls
			// must give a bounded verdict even when no release record exists yet.
			for poll := 0; poll < 2; poll++ {
				got, err := r.Status(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				if got == nil || got.ProgressResult == nil || got.ProgressResult.OperationStatus != resource.OperationStatusFailure || !strings.Contains(got.ProgressResult.StatusMessage, "deadline exhausted") {
					t.Fatalf("expired unfinished action must fail: %#v", got)
				}
				if _, ok := reserveFlight(scope, "ns", "release", inflight{generation: "competitor"}); ok {
					t.Fatal("deadline verdict released a live worker's ownership")
				}
			}
			if cancellations.Load() == 0 {
				t.Fatal("deadline did not cancel worker")
			}
			if storageReads.Load() != 0 {
				t.Fatal("deadline verdict waited for release storage")
			}
			if own := lookupFlight(scope, "ns", "release"); own == nil || own.finished {
				t.Fatal("unfinished worker lost cancellation/ownership fence")
			}
			finishFlight(scope, "ns", "release", f.generation, context.DeadlineExceeded)
			got, err := r.Status(context.Background(), req)
			if err != nil || got.ProgressResult.OperationStatus != resource.OperationStatusFailure {
				t.Fatalf("stopped worker outcome: %v %v", got, err)
			}
			if _, ok := reserveFlight(scope, "ns", "release", inflight{generation: "after-stop"}); !ok {
				t.Fatal("reported stopped worker still excluded new work")
			}
		})
	}
}
