// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package auth

import "k8s.io/client-go/rest"

// AuthProvider configures a rest.Config with provider-specific authentication.
// Implementations may set bearer tokens via WrapTransport, client certificates,
// or any other auth mechanism supported by client-go.
type AuthProvider interface {
	ConfigureTransport(cfg *rest.Config) error
}
