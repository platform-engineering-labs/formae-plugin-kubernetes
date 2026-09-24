//go:build unit

// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ergo.services/ergo"
	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"
	"github.com/google/uuid"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/model"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	clocktesting "k8s.io/utils/clock/testing"
)

// Only Namespace and Read are used by this real operator invocation. Embedding
// the interface makes an unexpected call fail rather than granting fake behavior.
type allowanceContextCapture struct {
	plugin.FullResourcePlugin
	contexts chan context.Context
}

func (*allowanceContextCapture) Namespace() string { return "K8S" }
func (p *allowanceContextCapture) Read(ctx context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	p.contexts <- ctx
	return &resource.ReadResult{Properties: `{}`}, nil
}

type allowanceReadRequest struct{}
type allowanceRequester struct {
	act.Actor
	operator gen.PID
	done     chan error
}

func (a *allowanceRequester) HandleMessage(_ gen.PID, message any) error {
	if _, ok := message.(allowanceReadRequest); ok {
		_, err := a.CallWithTimeout(a.operator, plugin.ReadResource{Namespace: "K8S", ResourceType: "K8S::Test::Context", NativeID: "context"}, 5)
		a.done <- err
	}
	return nil
}

// Obtain the private trusted context through the public SDK/actor protocol.
// No private context-key forgery, SDK edits, broker or cloud requests are used.
func trustedAllowanceContext(t *testing.T, info plugin.OidcOperationInfo) context.Context {
	t.Helper()
	node, err := ergo.StartNode(gen.Atom("allowance-"+uuid.NewString()+"@localhost"), gen.NodeOptions{
		Network: gen.NetworkOptions{Mode: gen.NetworkModeDisabled},
		Log:     gen.LogOptions{DefaultLogger: gen.DefaultLoggerOptions{Disable: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(node.Stop)
	capture := &allowanceContextCapture{contexts: make(chan context.Context, 1)}
	base := context.WithValue(context.Background(), helmOperationKey{}, "boundary")
	operator, err := node.Spawn(plugin.NewPluginOperator, gen.ProcessOptions{Env: map[gen.Env]any{
		"Plugin": capture, "Context": base,
		"RetryConfig":              model.RetryConfig{MaxRetries: 0, StatusCheckInterval: info.PollInterval, RetryDelay: info.RetryDelay},
		"OidcCredentialBrokerNode": string(node.Name()), "OidcCredentialBrokerName": "unused-test-broker",
		"OidcOperationBindingID":        info.BindingID,
		"OidcOperationPollInterval":     info.PollInterval,
		"OidcOperationCallTimeout":      info.CallTimeout,
		"OidcOperationRetryDelay":       info.RetryDelay,
		"OidcOperationThrottleMaxDelay": info.ThrottleMaxDelay,
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	requester, err := node.Spawn(func() gen.ProcessBehavior { return &allowanceRequester{operator: operator, done: done} }, gen.ProcessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = node.Send(requester, allowanceReadRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("SDK Read protocol: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("SDK Read protocol did not finish")
	}
	select {
	case ctx := <-capture.contexts:
		actual, ok := plugin.OidcOperationMetadata(ctx)
		if !ok || actual != info {
			t.Fatalf("SDK metadata mismatch: %#v %v", actual, ok)
		}
		if ctx.Value(helmOperationKey{}) != "boundary" {
			t.Fatal("SDK dropped base context")
		}
		return ctx
	default:
		t.Fatal("SDK Read did not capture context")
		return nil
	}
}

func TestAllowanceTrustedSDKContext(t *testing.T) {
	if _, ok := plugin.OidcOperationMetadata(context.Background()); ok {
		t.Fatal("untrusted context accepted")
	}
	trustedAllowanceContext(t, bridgeInfo())
}

func TestReleaseMinimumAllowance(t *testing.T) {
	for _, operation := range []string{"Create", "Update"} {
		for _, seconds := range []int{129, 130, 131} {
			t.Run(fmt.Sprintf("%s/%ds", operation, seconds), func(t *testing.T) {
				clearFlights()
				defer clearFlights()
				var uidReads, storageReads, mutations atomic.Int32
				const marker = "boundary-storage-reached"
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" {
						mutations.Add(1)
						t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
					}
					if r.Header.Get("Authorization") != "Bearer "+helmJWT("boundary") {
						t.Error("missing callback-owned fake credential")
					}
					w.Header().Set("Content-Type", "application/json")
					switch {
					case r.URL.Path == "/api/v1/namespaces/kube-system":
						uidReads.Add(1)
						fmt.Fprint(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"kube-system","uid":"boundary-uid"}}`)
					case strings.HasSuffix(r.URL.Path, "/secrets"):
						storageReads.Add(1)
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","message":%q,"code":403}`, marker)
					default:
						t.Errorf("unexpected GET %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				cfg := helmOidcConfig(t, server)
				r := testReleaseForConfig(t, cfg)
				ctx := trustedAllowanceContext(t, bridgeInfo())
				ctx, operationCancel := config.NewOperationContext(ctx)
				defer operationCancel()
				props := []byte(fmt.Sprintf(`{"metadata":{"name":"boundary","namespace":"ns"},"chart":"unused-before-storage","timeoutSeconds":%d}`, seconds))
				bounded, cancel, err := NewCallbackContext(ctx, cfg, props)
				if seconds == 129 {
					if cancel != nil {
						cancel()
					}
					if err == nil || !strings.Contains(err.Error(), "timeoutSeconds") {
						t.Fatalf("short preflight error=%v", err)
					}
					if uidReads.Load() != 0 || storageReads.Load() != 0 || mutations.Load() != 0 {
						t.Fatal("short preflight reached API")
					}
					t.Log("129s rejected at callback boundary with zero API requests")
					return
				}
				if err != nil {
					t.Fatalf("configured allowance rejected at callback boundary: %v", err)
				}
				defer cancel()
				if operation == "Create" {
					_, err = r.Create(bounded, &resource.CreateRequest{ResourceType: ResourceTypeRelease, Properties: props})
				} else {
					_, err = r.Update(bounded, &resource.UpdateRequest{ResourceType: ResourceTypeRelease, NativeID: "ns/boundary", DesiredProperties: props})
				}
				if err == nil || !strings.Contains(err.Error(), marker) {
					t.Errorf("expected intentional storage stop; got %v", err)
				}
				if uidReads.Load() != 1 || storageReads.Load() != 1 || mutations.Load() != 0 {
					t.Errorf("UID=%d storage=%d mutations=%d; want 1/1/0", uidReads.Load(), storageReads.Load(), mutations.Load())
				}
				if f := lookupFlight("UID|boundary-uid", "ns", "boundary"); f != nil {
					t.Error("preparation failure retained flight")
				}
				t.Logf("configured=%ds UID=%d storage=%d mutations=%d error=%v", seconds, uidReads.Load(), storageReads.Load(), mutations.Load(), err)
			})
		}
	}
}

func TestStatusUsesOriginalActionAllowance(t *testing.T) {
	for _, changedTiming := range []bool{false, true} {
		name := "minimum_with_short_timestamp_span"
		if changedTiming {
			name = "larger_requirement_with_inflated_timestamp_span"
		}
		t.Run(name, func(t *testing.T) {
			clearFlights()
			defer clearFlights()
			var uidReads, storageReads, mutations atomic.Int32
			const marker = "allowance-status-storage-reached"
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mutations.Add(1)
					t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer "+helmJWT("boundary") {
					t.Error("wrong callback credential")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/namespaces/kube-system" {
					uidReads.Add(1)
					fmt.Fprint(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"kube-system","uid":"allowance-status"}}`)
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/secrets") {
					t.Errorf("unexpected GET %s", r.URL.Path)
				}
				storageReads.Add(1)
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","message":%q,"code":403}`, marker)
			}))
			defer server.Close()
			cfg := helmOidcConfig(t, server)
			r := testReleaseForConfig(t, cfg)
			info := bridgeInfo()
			if changedTiming {
				info.PollInterval = 31 * time.Second
			}
			ctx := trustedAllowanceContext(t, info)
			ctx, cancel := config.NewOperationContext(ctx)
			defer cancel()
			identity, binding, err := flightIdentity(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			c := clocktesting.NewFakeClock(now)
			bridge, err := newAuthBridgeWithClock(bridgeInfo(), identity, 130*time.Second, now.Add(130*time.Second), c)
			if err != nil {
				t.Fatal(err)
			}
			started := now.Add(time.Nanosecond)
			if changedTiming {
				started = now.Add(-time.Minute)
			}
			f := inflight{op: opUpgrade, revision: 1, generation: uuid.NewString(), authFingerprint: identity, bindingID: binding, bridge: bridge, started: started, deadline: bridge.deadline}
			if _, ok := reserveFlight("UID|allowance-status", "ns", "boundary", f); !ok {
				t.Fatal("reserve flight")
			}
			// Service would change the bridge timestamp, exposing whether the
			// changed timing contract was rejected before feeding the worker.
			c.Step(time.Second)
			_, err = r.Status(ctx, &resource.StatusRequest{NativeID: "ns/boundary", RequestID: flightRequestID("ns", "boundary", &f)})
			bridge.mu.Lock()
			serviced := bridge.serviced
			bridge.mu.Unlock()
			if changedTiming {
				if err == nil || !strings.Contains(err.Error(), "timeoutSeconds") {
					t.Errorf("larger timing requirement must reject original allowance: %v", err)
				}
				if storageReads.Load() != 0 || !serviced.Equal(now) {
					t.Errorf("invalid allowance reached service/storage: serviced=%s reads=%d", serviced, storageReads.Load())
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), marker) {
					t.Errorf("valid original allowance did not reach storage: %v", err)
				}
				if storageReads.Load() != 1 || !serviced.Equal(c.Now()) {
					t.Errorf("valid allowance failed to service/read: serviced=%s reads=%d", serviced, storageReads.Load())
				}
			}
			if uidReads.Load() != 1 || mutations.Load() != 0 {
				t.Errorf("UID reads=%d mutations=%d", uidReads.Load(), mutations.Load())
			}
			remaining := lookupFlight("UID|allowance-status", "ns", "boundary")
			if remaining == nil || remaining.generation != f.generation || remaining.statusReaders != 0 || !remaining.started.Equal(f.started) || !remaining.deadline.Equal(f.deadline) {
				t.Errorf("Status changed flight ownership or timing: %#v", remaining)
			}
		})
	}
}
