// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package gke

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
	container "google.golang.org/api/container/v1"
)

// describeTimeout caps ADC lookup plus one clusters.get call.
const describeTimeout = 15 * time.Second

// DescribeCluster returns the cluster's API server endpoint and CA bundle
// from the GKE control plane, so a target need only name the project,
// location and cluster.
//
// Requires container.clusters.get, in addition to whatever RBAC the caller
// holds inside the cluster.
func (p *Provider) DescribeCluster(ctx context.Context) (string, []byte, error) {
	missing := ""
	switch {
	case p.ProjectID == "":
		missing = "ProjectId"
	case p.Location == "":
		missing = "Location"
	case p.ClusterName == "":
		missing = "ClusterName"
	}
	if missing != "" {
		return "", nil, fmt.Errorf(
			"GKE auth: %s is required to look up the cluster's endpoint and CA — "+
				"set ProjectId, Location and ClusterName, or set Endpoint and "+
				"CertificateAuthority explicitly", missing)
	}

	ctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()

	log := plugin.LoggerFromContext(ctx).With("auth", "GKE", "cluster", p.ClusterName)

	svc, err := container.NewService(ctx)
	if err != nil {
		return "", nil, fmt.Errorf(
			"GKE auth: GCP credentials not found — run `gcloud auth application-default login`, "+
				"or set GOOGLE_APPLICATION_CREDENTIALS: %w", err)
	}

	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", p.ProjectID, p.Location, p.ClusterName)
	cluster, err := svc.Projects.Locations.Clusters.Get(name).Context(ctx).Do()
	if err != nil {
		return "", nil, fmt.Errorf(
			"GKE auth: get %s failed (needs container.clusters.get): %w", name, err)
	}
	if cluster.Endpoint == "" {
		return "", nil, fmt.Errorf("GKE auth: cluster %s reports no endpoint", name)
	}
	if cluster.MasterAuth == nil || cluster.MasterAuth.ClusterCaCertificate == "" {
		return "", nil, fmt.Errorf("GKE auth: cluster %s reports no cluster CA certificate", name)
	}

	caData, err := base64.StdEncoding.DecodeString(cluster.MasterAuth.ClusterCaCertificate)
	if err != nil {
		return "", nil, fmt.Errorf("GKE auth: cluster %s returned an undecodable CA: %w", name, err)
	}

	log.Debug("derived GKE connection details", "endpoint", cluster.Endpoint)
	return cluster.Endpoint, caData, nil
}
