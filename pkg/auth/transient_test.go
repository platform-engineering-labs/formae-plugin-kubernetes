//go:build unit

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"ergo.services/ergo/gen"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/aws/smithy-go"
	"github.com/platform-engineering-labs/formae/pkg/credential"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"golang.org/x/oauth2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type statusError int

func (statusError) Error() string         { return "SECRET" }
func (e statusError) HTTPStatusCode() int { return int(e) }
func TestTransientErrorsSurviveRedactionWithoutExposingCause(t *testing.T) {
	for _, tc := range []struct {
		err   error
		retry bool
	}{
		{context.DeadlineExceeded, true}, {context.Canceled, true}, {credential.ErrBrokerUnavailable, true}, {credential.ErrMintFailed, true}, {gen.ErrTimeout, true}, {gen.ErrNoConnection, true},
		{plugin.ErrNoOidcBroker, false}, {credential.ErrInvalidAudience, false}, {credential.ErrInternal, false}, {errors.New("invalid config SECRET"), false},
		{apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "kube-system", errors.New("SECRET")), false},
		{apierrors.NewUnauthorized("SECRET"), false}, {apierrors.NewInternalError(errors.New("SECRET")), true},
		{&smithy.GenericAPIError{Code: "ThrottlingException"}, true}, {&smithy.GenericAPIError{Code: "AccessDenied"}, false}, {statusError(503), true}, {statusError(403), false},
		{&azcore.ResponseError{StatusCode: 429}, true}, {&azcore.ResponseError{StatusCode: 403}, false},
		{&azidentity.AuthenticationFailedError{RawResponse: &http.Response{StatusCode: 503}}, true},
		{&azidentity.AuthenticationFailedError{RawResponse: &http.Response{StatusCode: 400}}, false},
		{&oauth2.RetrieveError{Response: &http.Response{StatusCode: 502}, Body: []byte("SECRET")}, true},
		{&oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}, Body: []byte("SECRET")}, false},
	} {
		for _, err := range []error{tc.err, RedactError(fmt.Errorf("SECRET: %w", tc.err)), RedactError(RedactError(tc.err))} {
			if got := IsTransient(err); got != tc.retry {
				t.Errorf("%T transient=%t want %t", tc.err, got, tc.retry)
			}
		}
		redacted := RedactError(tc.err)
		if errors.Unwrap(redacted) != nil || strings.Contains(fmt.Sprintf("%+v", redacted), "SECRET") {
			t.Fatal("redaction exposed cause")
		}
		var status interface{ HTTPStatusCode() int }
		if errors.As(redacted, &status) {
			t.Fatal("redaction exposed typed cause")
		}
	}
}
