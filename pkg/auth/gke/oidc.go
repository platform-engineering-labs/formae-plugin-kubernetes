// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package gke

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/internal/federation"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google/externalaccount"
)

type oidcSource struct {
	source                        plugin.OidcTokenSource
	provider, serviceAccountEmail string
	client                        *http.Client
}

// NewOidcTokenSource uses only an explicit broker supplier. OAuth sources holding
// a context are constructed and discarded within each Token call.
func NewOidcTokenSource(src plugin.OidcTokenSource, provider, serviceAccountEmail string) auth.TokenSource {
	return newOidcTokenSource(src, provider, serviceAccountEmail, nil)
}
func newOidcTokenSource(src plugin.OidcTokenSource, provider, serviceAccountEmail string, rt http.RoundTripper) auth.TokenSource {
	urls := []string{"https://sts.googleapis.com/v1/token"}
	if serviceAccountEmail != "" {
		urls = append(urls, impersonationURL(serviceAccountEmail))
	}
	return &oidcSource{source: src, provider: provider, serviceAccountEmail: serviceAccountEmail, client: federation.Client(rt, urls...)}
}
func impersonationURL(email string) string {
	return "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + email + ":generateAccessToken"
}
func (s *oidcSource) SubjectToken(ctx context.Context, opts externalaccount.SupplierOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.source == nil {
		return "", plugin.ErrNoOidcBroker
	}
	token, err := s.source.IdentityToken(ctx, opts.Audience)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", auth.RedactError(err)
	}
	if token == "" {
		return "", errors.New("empty identity assertion")
	}
	return token, nil
}
func (s *oidcSource) Token(ctx context.Context) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.client)
	config := externalaccount.Config{Audience: s.provider, SubjectTokenType: "urn:ietf:params:oauth:token-type:jwt", TokenURL: "https://sts.googleapis.com/v1/token", Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"}, SubjectTokenSupplier: s}
	if s.serviceAccountEmail != "" {
		config.ServiceAccountImpersonationURL = impersonationURL(s.serviceAccountEmail)
		config.Scopes = append(config.Scopes, "https://www.googleapis.com/auth/userinfo.email")
	}
	source, err := externalaccount.NewTokenSource(ctx, config)
	if err != nil {
		return "", time.Time{}, auth.RedactError(err)
	}
	token, err := source.Token()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", time.Time{}, auth.RedactError(err)
	}
	if token.AccessToken == "" || !token.Expiry.After(time.Now().Add(10*time.Second)) {
		return "", time.Time{}, errors.New("empty or expired Google access token")
	}
	return token.AccessToken, token.Expiry, nil
}
