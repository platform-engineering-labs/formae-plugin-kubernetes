// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package ovh

import (
	"context"
	"fmt"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

// DescribeCluster returns the cluster's API server endpoint and CA bundle.
//
// OVH already hands the provider a whole kubeconfig on every token mint —
// this reuses that same call and keeps the two fields the token path throws
// away, so a target need only name the service and cluster.
func (p *Provider) DescribeCluster(ctx context.Context) (string, []byte, error) {
	if p.ServiceName == "" || p.ClusterID == "" {
		return "", nil, fmt.Errorf(
			"OVH auth: ServiceName and ClusterId are required to look up the " +
				"cluster's endpoint and CA")
	}

	kc, err := fetchKubeconfig(ctx, p.ServiceName, p.ClusterID)
	if err != nil {
		return "", nil, err
	}
	if kc.Host == "" || len(kc.CAData) == 0 {
		return "", nil, fmt.Errorf(
			"OVH auth: kubeconfig for cluster %q carries no server URL or CA", p.ClusterID)
	}

	plugin.LoggerFromContext(ctx).With("auth", "OVH", "cluster", p.ClusterID).
		Debug("derived OVH connection details", "endpoint", kc.Host)
	return kc.Host, kc.CAData, nil
}
