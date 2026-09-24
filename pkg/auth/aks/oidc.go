// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package aks

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/internal/federation"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

// assertionFailure is call-local: azidentity does not unwrap callback errors.
// Carry the original cause through the live context to preserve errors.Is.
type assertionFailureKey struct{}
type assertionFailure struct {
	mu  sync.Mutex
	err error
}

func (f *assertionFailure) set(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.err = err }
func (f *assertionFailure) get() error    { f.mu.Lock(); defer f.mu.Unlock(); return f.err }

type oidcSource struct {
	credential *azidentity.ClientAssertionCredential
	err        error
}

// NewOidcTokenSource retains a context-free assertion credential and its in-memory
// MSAL cache. Every assertion callback receives the current GetToken context.
func NewOidcTokenSource(src plugin.OidcTokenSource, tenantID, clientID string) auth.TokenSource {
	return newOidcTokenSource(src, tenantID, clientID, nil)
}
func newOidcTokenSource(src plugin.OidcTokenSource, tenantID, clientID string, rt http.RoundTripper) auth.TokenSource {
	if src == nil {
		return &oidcSource{err: plugin.ErrNoOidcBroker}
	}
	authority := "https://login.microsoftonline.com/" + tenantID
	client := federation.Client(rt, authority+"/v2.0/.well-known/openid-configuration", authority+"/oauth2/v2.0/token")
	cred, err := azidentity.NewClientAssertionCredential(tenantID, clientID, func(ctx context.Context) (assertion string, err error) {
		defer func() {
			if err != nil {
				if failure, ok := ctx.Value(assertionFailureKey{}).(*assertionFailure); ok {
					failure.set(err)
				}
				err = auth.RedactError(err)
			}
		}()
		if err := ctx.Err(); err != nil {
			return "", err
		}
		assertion, err = src.IdentityToken(ctx, "api://AzureADTokenExchange")
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			return "", err
		}
		if assertion == "" {
			return "", errors.New("empty identity assertion")
		}
		return assertion, nil
	}, &azidentity.ClientAssertionCredentialOptions{
		ClientOptions: azcore.ClientOptions{
			// An empty Cloud would read AZURE_AUTHORITY_HOST.
			Cloud:     cloud.AzurePublic,
			Transport: client,
			Retry:     policy.RetryOptions{MaxRetries: -1},
		},
		// The authority is fixed to public Azure and the transport also constrains
		// the discovery document's token endpoint to this exact tenant.
		DisableInstanceDiscovery: true,
	})
	return &oidcSource{credential: cred, err: auth.RedactError(err)}
}
func (s *oidcSource) Token(ctx context.Context) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	if s.err != nil {
		return "", time.Time{}, s.err
	}
	failure := &assertionFailure{}
	ctx = context.WithValue(ctx, assertionFailureKey{}, failure)
	token, err := s.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{DefaultAKSScope}})
	if brokerErr := failure.get(); brokerErr != nil {
		err = brokerErr
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", time.Time{}, auth.RedactError(err)
	}
	if token.Token == "" || !token.ExpiresOn.After(time.Now().Add(10*time.Second)) {
		return "", time.Time{}, errors.New("empty or expired Azure access token")
	}
	return token.Token, token.ExpiresOn, nil
}
