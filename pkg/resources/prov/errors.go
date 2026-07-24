// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// authTokenErrorMarker is the prefix the cloud-auth token transport
// (pkg/auth/transport.go) attaches to a credential/token retrieval failure
// before it is wrapped by net/http in a *url.Error. Because that *url.Error
// satisfies the net.Error interface, an auth failure is indistinguishable from
// a real network failure at the interface level — this marker is the only
// signal that separates "couldn't get a token" (the cluster may be perfectly
// healthy) from "couldn't reach the apiserver". Kept in sync with transport.go.
const authTokenErrorMarker = "auth: failed to obtain token"

// ClassifyReadError maps a client-side transport failure from a resource Read
// to the health-signal error code the formae target reaper keys off:
// ServiceTimeout for a genuine dial/read deadline, NetworkFailure for a
// no-response transport failure (connection refused, DNS failure). It returns
// ok=false for anything else — application errors, and crucially auth/credential
// failures — so the caller leaves them as raw errors (rendered UnforeseenError)
// and no healthy cluster is ever reaped over a bad credential.
//
// It deliberately does NOT trust the net.Error interface: the cloud-auth
// providers inject tokens via rest.Config.WrapTransport, so a token failure
// surfaces as a *url.Error, which implements net.Error even though nothing on
// the network actually failed. Matching must therefore unwrap to CONCRETE net
// types (*net.OpError, *net.DNSError, syscall.ECONNREFUSED) and to the deadline
// sentinels — never to the interface.
func ClassifyReadError(err error) (resource.OperationErrorCode, bool) {
	if err == nil {
		return "", false
	}

	// Auth-path failures come first: a credential/token error (including an
	// auth-side context deadline against a hung IdP) must never be read as the
	// apiserver being unreachable. Excluded before the deadline/net checks
	// below, which would otherwise catch the auth-wrapped deadline or net error.
	if strings.Contains(err.Error(), authTokenErrorMarker) {
		return "", false
	}

	// A genuine apiserver dial/read deadline.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return resource.OperationErrorCodeServiceTimeout, true
	}

	// Concrete transport failures. errors.As unwraps through the *url.Error the
	// http client wraps around the RoundTripper error to reach the real net type.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return resource.OperationErrorCodeServiceTimeout, true
		}
		return resource.OperationErrorCodeNetworkFailure, true
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return resource.OperationErrorCodeNetworkFailure, true
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return resource.OperationErrorCodeNetworkFailure, true
	}

	return "", false
}
