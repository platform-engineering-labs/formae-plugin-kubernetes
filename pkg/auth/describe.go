// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package auth

import "context"

// ClusterDescriber is implemented by an AuthProvider that can look its
// cluster's connection details up from the cloud, so a target only has to
// name the cluster instead of restating what the cloud already knows.
//
// A target that spells out Endpoint and CertificateAuthority keeps them: the
// explicit values always win, which is what an air-gapped cluster, a custom
// endpoint, or anything formae does not model needs. Derivation fills the
// gap when either is absent.
//
// caData is the decoded PEM bundle, not the base64 form the cloud APIs
// return — callers assign it straight to rest.Config.TLSClientConfig.CAData.
type ClusterDescriber interface {
	DescribeCluster(ctx context.Context) (endpoint string, caData []byte, err error)
}
