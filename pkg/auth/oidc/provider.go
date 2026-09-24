// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Package oidc supplies direct Kubernetes bearer tokens. It reads the JWT exp
// claim for refresh scheduling only; the Kubernetes API server verifies the
// signature, issuer, audience, and authorization.
package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/internal/kubernetesaudience"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

type provider struct {
	source   plugin.OidcTokenSource
	audience string
}

// NewProvider retains only the source and validated identity coordinates, never
// an operation context. Invalid audiences fail before any broker call.
func NewProvider(src plugin.OidcTokenSource, audience string) auth.TokenSource {
	return &provider{source: src, audience: audience}
}

func (p *provider) Token(ctx context.Context) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	if !validAudience(p.audience) {
		return "", time.Time{}, errors.New("invalid Kubernetes OIDC audience")
	}
	if p.source == nil {
		return "", time.Time{}, plugin.ErrNoOidcBroker
	}
	token, err := p.source.IdentityToken(ctx, p.audience)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", time.Time{}, auth.RedactError(err)
	}
	expiry, err := tokenExpiry(token, time.Now())
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiry, nil
}

func validAudience(audience string) bool {
	return kubernetesaudience.Valid(audience)
}

func tokenExpiry(token string, now time.Time) (time.Time, error) {
	malformed := errors.New("malformed or expired OIDC identity token")
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return time.Time{}, malformed
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	var headerFields map[string]json.RawMessage
	if err != nil || json.Unmarshal(header, &headerFields) != nil || headerFields == nil {
		return time.Time{}, malformed
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		return time.Time{}, malformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, malformed
	}
	var claims map[string]json.RawMessage
	var exp float64
	if err = json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, malformed
	}
	if err = json.Unmarshal(claims["exp"], &exp); err != nil || math.IsNaN(exp) || math.IsInf(exp, 0) || exp <= 0 || exp > 253402300799 {
		return time.Time{}, malformed
	}
	seconds, fraction := math.Modf(exp)
	expiry := time.Unix(int64(seconds), int64(fraction*1e9))
	if !now.Add(10 * time.Second).Before(expiry) {
		return time.Time{}, malformed
	}
	return expiry, nil
}
