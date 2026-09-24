// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package eks

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/internal/federation"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

var commercialRegionPattern = regexp.MustCompile(`^(af|ap|ca|eu|il|me|mx|sa|us)-(central|east|north|northeast|northwest|south|southeast|southwest|west)-[0-9]+$`)

// IsCommercialRegion accepts commercial AWS region coordinates only. Additional
// region families require an explicit update; sovereign partitions fail closed.
func IsCommercialRegion(region string) bool { return commercialRegionPattern.MatchString(region) }

type oidcSource struct {
	source                       plugin.OidcTokenSource
	roleARN, region, clusterName string
	client                       *sts.Client
	now                          func() time.Time
}

// NewOidcTokenSource exchanges installation identity for explicit role credentials.
// It never consults an AWS credential chain or retains an operation context.
func NewOidcTokenSource(src plugin.OidcTokenSource, roleARN, region, clusterName string) auth.TokenSource {
	return newOidcTokenSource(src, roleARN, region, clusterName, nil, time.Now)
}
func newOidcTokenSource(src plugin.OidcTokenSource, roleARN, region, clusterName string, rt http.RoundTripper, now func() time.Time) auth.TokenSource {
	endpoint := "https://sts." + region + ".amazonaws.com/"
	return &oidcSource{source: src, roleARN: roleARN, region: region, clusterName: clusterName, now: now, client: sts.New(sts.Options{Region: region, BaseEndpoint: aws.String(endpoint), HTTPClient: federation.Client(rt, endpoint), RetryMaxAttempts: 1})}
}
func (s *oidcSource) Token(ctx context.Context) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	if !IsCommercialRegion(s.region) {
		return "", time.Time{}, errors.New("invalid commercial AWS region")
	}
	if s.source == nil {
		return "", time.Time{}, plugin.ErrNoOidcBroker
	}
	assertion, err := s.source.IdentityToken(ctx, "sts.amazonaws.com")
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", time.Time{}, auth.RedactError(err)
	}
	if assertion == "" {
		return "", time.Time{}, errors.New("empty identity assertion")
	}
	result, err := s.client.AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{RoleArn: aws.String(s.roleARN), RoleSessionName: aws.String("formae-kubernetes"), WebIdentityToken: aws.String(assertion)})
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", time.Time{}, auth.RedactError(err)
	}
	signedAt := s.now()
	if result.Credentials == nil || result.Credentials.Expiration == nil || aws.ToString(result.Credentials.AccessKeyId) == "" || aws.ToString(result.Credentials.SecretAccessKey) == "" || aws.ToString(result.Credentials.SessionToken) == "" {
		return "", time.Time{}, errors.New("incomplete AWS temporary credentials")
	}
	// aws-iam-authenticator validates timestamp + 15 minutes independently of
	// X-Amz-Expires=60. Keep its one-minute cushion and also cap by STS expiry.
	// https://github.com/kubernetes-sigs/aws-iam-authenticator/blob/master/pkg/token/token.go
	expiry := signedAt.Add(14 * time.Minute)
	if credentialExpiry := result.Credentials.Expiration.Add(-time.Minute); credentialExpiry.Before(expiry) {
		expiry = credentialExpiry
	}
	if !expiry.After(signedAt.Add(10 * time.Second)) {
		return "", time.Time{}, errors.New("AWS temporary credentials expire too soon")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://sts."+s.region+".amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15&X-Amz-Expires=60", nil)
	if err != nil {
		return "", time.Time{}, auth.RedactError(err)
	}
	req.Header.Set("x-k8s-aws-id", s.clusterName)
	creds := aws.Credentials{AccessKeyID: aws.ToString(result.Credentials.AccessKeyId), SecretAccessKey: aws.ToString(result.Credentials.SecretAccessKey), SessionToken: aws.ToString(result.Credentials.SessionToken), CanExpire: true, Expires: *result.Credentials.Expiration}
	signed, _, err := v4.NewSigner().PresignHTTP(ctx, creds, req, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "sts", s.region, signedAt)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", time.Time{}, auth.RedactError(err)
	}
	return "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString([]byte(signed)), expiry, nil
}
