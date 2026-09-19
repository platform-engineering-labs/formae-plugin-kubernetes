// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"ergo.services/ergo/gen"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/aws/smithy-go"
	"github.com/platform-engineering-labs/formae/pkg/credential"
	"golang.org/x/oauth2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// ErrTransient exposes retry classification through redaction without exposing
// a credential-bearing cause via Unwrap or As. Unknown errors stay terminal.
var ErrTransient = errors.New("temporary credential or API service failure")

func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTransient) {
		return true
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded, credential.ErrBrokerUnavailable, credential.ErrMintFailed, gen.ErrTimeout, gen.ErrNoConnection, io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	if apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || apierrors.IsTooManyRequests(err) || apierrors.IsServiceUnavailable(err) || apierrors.IsInternalError(err) {
		return true
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsTemporary {
		return true
	}
	var aws smithy.APIError
	if errors.As(err, &aws) {
		switch aws.ErrorCode() {
		case "Throttling", "ThrottlingException", "RequestLimitExceeded", "TooManyRequestsException":
			return true
		}
	}
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) {
		return transientStatus(status.HTTPStatusCode())
	}
	var azure *azcore.ResponseError
	if errors.As(err, &azure) {
		return transientStatus(azure.StatusCode)
	}
	var identity *azidentity.AuthenticationFailedError
	if errors.As(err, &identity) && identity.RawResponse != nil {
		return transientStatus(identity.RawResponse.StatusCode)
	}
	var oauth *oauth2.RetrieveError
	return errors.As(err, &oauth) && oauth.Response != nil && transientStatus(oauth.Response.StatusCode)
}
func transientStatus(status int) bool {
	return status == 408 || status == 429 || status >= 500 && status <= 599
}
