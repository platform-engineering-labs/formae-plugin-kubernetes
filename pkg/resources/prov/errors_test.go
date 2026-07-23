// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package prov

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
)

// timeoutErr is a concrete error that reports itself as a timeout, used to
// build a net.OpError whose Timeout() is true (the shape a dial/read deadline
// takes at the socket layer).
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// urlErr wraps inner the way net/http's client wraps any RoundTripper error —
// this is what actually reaches the plugin from a client-go request.
func urlErr(inner error) *url.Error {
	return &url.Error{Op: "Get", URL: "https://10.0.0.1:443/api", Err: inner}
}

func TestClassifyReadError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode resource.OperationErrorCode
		wantOK   bool
	}{
		{
			name:   "nil error is not classified",
			err:    nil,
			wantOK: false,
		},
		{
			name:     "connection refused, wrapped in url.Error, is a network failure",
			err:      urlErr(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}),
			wantCode: resource.OperationErrorCodeNetworkFailure,
			wantOK:   true,
		},
		{
			name:     "bare connection-refused OpError is a network failure",
			err:      &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			wantCode: resource.OperationErrorCodeNetworkFailure,
			wantOK:   true,
		},
		{
			name:     "DNS resolution failure is a network failure",
			err:      urlErr(&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "api.cluster", IsNotFound: true}}),
			wantCode: resource.OperationErrorCodeNetworkFailure,
			wantOK:   true,
		},
		{
			name:     "dial/read timeout (OpError.Timeout) is a service timeout",
			err:      urlErr(&net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}),
			wantCode: resource.OperationErrorCodeServiceTimeout,
			wantOK:   true,
		},
		{
			name:     "apiserver context deadline is a service timeout",
			err:      urlErr(context.DeadlineExceeded),
			wantCode: resource.OperationErrorCodeServiceTimeout,
			wantOK:   true,
		},
		{
			name:     "os.ErrDeadlineExceeded is a service timeout",
			err:      urlErr(os.ErrDeadlineExceeded),
			wantCode: resource.OperationErrorCodeServiceTimeout,
			wantOK:   true,
		},
		// --- The auth trap: a credential/token failure surfaces as a *url.Error
		// (which satisfies net.Error) carrying the "auth:" marker. A healthy
		// cluster with a bad credential MUST NOT be classified as unreachable.
		{
			name:   "auth token retrieval failure is NOT unreachable",
			err:    urlErr(fmt.Errorf("auth: failed to obtain token: %w", errors.New("NoCredentialProviders: no valid providers"))),
			wantOK: false,
		},
		{
			name:   "auth-path deadline is NOT a service timeout (hung IdP, healthy apiserver)",
			err:    urlErr(fmt.Errorf("auth: failed to obtain token: %w", context.DeadlineExceeded)),
			wantOK: false,
		},
		{
			name:   "auth-path connection-refused (IdP endpoint) is NOT unreachable",
			err:    urlErr(fmt.Errorf("auth: failed to obtain token: %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})),
			wantOK: false,
		},
		// --- Non-transport errors fall through: only genuine transport failures
		// are the reaper's unreachable signal.
		{
			name:   "a plain application error is not classified",
			err:    errors.New("the server could not find the requested resource"),
			wantOK: false,
		},
		{
			name:   "a url.Error wrapping a plain error is not classified (interface not trusted)",
			err:    urlErr(errors.New("unexpected EOF")),
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := ClassifyReadError(tc.err)
			assert.Equal(t, tc.wantOK, ok, "classification ok flag")
			if tc.wantOK {
				assert.Equal(t, tc.wantCode, code, "classified error code")
			} else {
				assert.Equal(t, resource.OperationErrorCode(""), code, "unclassified must return empty code")
			}
		})
	}
}
