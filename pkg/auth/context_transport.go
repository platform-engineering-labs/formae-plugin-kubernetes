// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"io"
	"net/http"
	"sync"
)

// WithOperationContext adapts a call-owned contextless client to a live operation.
// Never cache or reuse this adapter across operations. Values come exclusively
// from the trusted operation; request deadlines and cancellation still apply.
// It grants no authority beyond the operation's lifetime.
func WithOperationContext(base http.RoundTripper, ctx context.Context) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &operationTransport{base: base, operation: ctx}
}

type operationTransport struct {
	base      http.RoundTripper
	operation context.Context
}

func (t *operationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.operation.Err(); err != nil {
		closeRequest(req)
		return nil, err
	}
	if err := req.Context().Err(); err != nil {
		closeRequest(req)
		return nil, err
	}
	var merged context.Context
	var cancel context.CancelFunc
	if deadline, ok := req.Context().Deadline(); ok {
		merged, cancel = context.WithDeadline(t.operation, deadline)
	} else {
		merged, cancel = context.WithCancel(t.operation)
	}
	stop := context.AfterFunc(req.Context(), cancel)
	var once sync.Once
	cleanup := func() { once.Do(func() { stop(); cancel() }) }
	if err := t.operation.Err(); err != nil {
		cleanup()
		closeRequest(req)
		return nil, err
	}
	if err := req.Context().Err(); err != nil {
		cleanup()
		closeRequest(req)
		return nil, err
	}
	resp, err := t.base.RoundTrip(req.Clone(merged))
	if err != nil {
		cleanup()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}
	if resp == nil || resp.Body == nil {
		cleanup()
		return resp, nil
	}
	// HTTP returns before a streaming body is consumed. Keep both cancellation
	// paths active until EOF/error or Close, not merely until RoundTrip returns.
	resp.Body = &operationBody{ReadCloser: resp.Body, cleanup: cleanup}
	return resp, nil
}

type operationBody struct {
	io.ReadCloser
	cleanup func()
}

func (b *operationBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.cleanup()
	}
	return n, err
}
func (b *operationBody) Close() error {
	defer b.cleanup()
	return b.ReadCloser.Close()
}
