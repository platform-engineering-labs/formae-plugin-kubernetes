// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package eks

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

// describeTimeout caps credential lookup plus one DescribeCluster call.
const describeTimeout = 15 * time.Second

// DescribeCluster returns the cluster's API server endpoint and CA bundle
// from the EKS control plane, so a target need only name the cluster and its
// region.
//
// Requires eks:DescribeCluster on the cluster, in addition to whatever the
// cluster's access entry grants inside Kubernetes.
func (p *Provider) DescribeCluster(ctx context.Context) (string, []byte, error) {
	if p.ClusterName == "" {
		return "", nil, fmt.Errorf("EKS auth: ClusterName is required to look up the cluster's endpoint and CA")
	}
	if p.Region == "" {
		return "", nil, fmt.Errorf(
			"EKS auth: Region is required to look up cluster %q — set Region, "+
				"or set Endpoint and CertificateAuthority explicitly", p.ClusterName)
	}

	ctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()

	log := plugin.LoggerFromContext(ctx).With("auth", "EKS", "cluster", p.ClusterName)

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(p.Region))
	if err != nil {
		return "", nil, fmt.Errorf("EKS auth: failed to load AWS config: %w", err)
	}

	out, err := eks.NewFromConfig(awsCfg).DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: &p.ClusterName,
	})
	if err != nil {
		return "", nil, fmt.Errorf(
			"EKS auth: DescribeCluster %q in %s failed (needs eks:DescribeCluster): %w",
			p.ClusterName, p.Region, err)
	}
	if out.Cluster == nil || out.Cluster.Endpoint == nil {
		return "", nil, fmt.Errorf("EKS auth: cluster %q reports no endpoint", p.ClusterName)
	}
	if out.Cluster.CertificateAuthority == nil || out.Cluster.CertificateAuthority.Data == nil {
		return "", nil, fmt.Errorf("EKS auth: cluster %q reports no certificate authority", p.ClusterName)
	}

	caData, err := base64.StdEncoding.DecodeString(*out.Cluster.CertificateAuthority.Data)
	if err != nil {
		return "", nil, fmt.Errorf("EKS auth: cluster %q returned an undecodable CA: %w", p.ClusterName, err)
	}

	log.Debug("derived EKS connection details", "endpoint", *out.Cluster.Endpoint)
	return *out.Cluster.Endpoint, caData, nil
}
