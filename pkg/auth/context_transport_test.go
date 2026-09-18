//go:build unit

package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type contextKey string

func TestOperationContextTrustedValuesAndEarliestDeadline(t *testing.T) {
	requestDeadline := time.Now().Add(time.Minute)
	requestCtx, requestCancel := context.WithDeadline(context.WithValue(context.Background(), contextKey("broker"), "untrusted"), requestDeadline)
	defer requestCancel()
	operationCtx, operationCancel := context.WithTimeout(context.WithValue(context.Background(), contextKey("broker"), "trusted"), 2*time.Minute)
	defer operationCancel()
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Context().Value(contextKey("broker")); got != "trusted" {
			t.Errorf("operation binding replaced: %v", got)
		}
		if d, ok := r.Context().Deadline(); !ok || !d.Equal(requestDeadline) {
			t.Errorf("request deadline lost: %v %v", d, ok)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	tr := WithOperationContext(base, operationCtx)
	req, _ := http.NewRequestWithContext(requestCtx, "GET", "https://cluster.invalid", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.Context().Value(contextKey("broker")) != "untrusted" {
		t.Fatal("caller context changed")
	}
}

func TestOperationContextCancellationWinsAndDoesNotMint(t *testing.T) {
	for _, which := range []string{"operation", "request"} {
		t.Run(which, func(t *testing.T) {
			op, cancelOp := context.WithCancel(context.Background())
			defer cancelOp()
			request, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			if which == "operation" {
				cancelOp()
			} else {
				cancelRequest()
			}
			calls := 0
			base := roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not run") })
			req, _ := http.NewRequestWithContext(request, "GET", "https://cluster.invalid", nil)
			_, err := WithOperationContext(base, op).RoundTrip(req)
			if !errors.Is(err, context.Canceled) || calls != 0 {
				t.Fatalf("canceled call reached transport: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestOperationContextRemainsLiveThroughTLSResponseBody(t *testing.T) {
	for _, which := range []string{"consume", "operation", "request"} {
		t.Run(which, func(t *testing.T) {
			op, cancelOp := context.WithCancel(context.Background())
			defer cancelOp()
			request, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			release := make(chan struct{})
			defer close(release)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "first")
				w.(http.Flusher).Flush()
				select {
				case <-release:
					_, _ = io.WriteString(w, "last")
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			req, _ := http.NewRequestWithContext(request, "GET", server.URL, nil)
			tr := WithOperationContext(server.Client().Transport, op)
			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			// Return from RoundTrip must leave the streaming body readable.
			buf := make([]byte, 5)
			if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != "first" {
				t.Fatalf("body canceled at RoundTrip return: %q %v", buf, err)
			}
			if which == "consume" {
				release <- struct{}{}
			} else if which == "operation" {
				cancelOp()
			} else {
				cancelRequest()
			}
			done := make(chan error, 1)
			go func() {
				body, err := io.ReadAll(resp.Body)
				if which == "consume" && string(body) != "last" {
					err = errors.New("lost response body")
				}
				done <- err
			}()
			select {
			case err := <-done:
				if which == "consume" && err != nil {
					t.Fatal(err)
				}
				if which != "consume" && err == nil {
					t.Fatal("cancellation did not reach body")
				}
			case <-time.After(time.Second):
				t.Fatal("body cancellation stuck")
			}
		})
	}
}

func TestOperationContextCleansUpOnBodyCloseAndTransportFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "body close", true: "failure"}[fail], func(t *testing.T) {
			var merged context.Context
			base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				merged = r.Context()
				if fail {
					return nil, errors.New("network failed")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})
			req, _ := http.NewRequest("GET", "https://cluster.invalid", nil)
			resp, _ := WithOperationContext(base, context.Background()).RoundTrip(req)
			if !fail {
				if merged.Err() != nil {
					t.Fatal("canceled before body close")
				}
				resp.Body.Close()
			}
			if merged.Err() == nil {
				t.Fatal("merged context leaked")
			}
		})
	}
}

func TestOperationContextEarlierOperationDeadlineAndEOFCleanup(t *testing.T) {
	operationDeadline := time.Now().Add(time.Minute)
	op, cancel := context.WithDeadline(context.Background(), operationDeadline)
	defer cancel()
	reqCtx, cancelReq := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelReq()
	var merged context.Context
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		merged = r.Context()
		if d, ok := merged.Deadline(); !ok || !d.Equal(operationDeadline) {
			t.Error("extended operation deadline")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("body"))}, nil
	})
	req, _ := http.NewRequestWithContext(reqCtx, "GET", "https://cluster.invalid", nil)
	resp, err := WithOperationContext(base, op).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if merged.Err() == nil {
		t.Fatal("EOF did not release context")
	}
	resp.Body.Close()
}
