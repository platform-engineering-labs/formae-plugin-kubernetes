// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/model"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	// Import resources to trigger init() registration
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/admissionregistration"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/apiextensions"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/apps"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/autoscaling"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/batch"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/coordination"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/core"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/flowcontrol"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/networking"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/node"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/policy"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/rbac"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/scheduling"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/storage"
)

// Plugin implements the Formae ResourcePlugin interface for Kubernetes.
// The SDK automatically provides identity methods (Name, Version, Namespace)
// by reading formae-plugin.pkl at startup.
type Plugin struct{}

// Compile-time check: Plugin must satisfy ResourcePlugin interface.
var _ plugin.ResourcePlugin = &Plugin{}

// =============================================================================
// Configuration Methods
// =============================================================================

// RateLimit returns the rate limiting configuration for this plugin.
// K8S API is generally more tolerant than cloud provider APIs.
func (p *Plugin) RateLimit() model.RateLimitConfig {
	return model.RateLimitConfig{
		Scope:                            model.RateLimitScopeNamespace,
		MaxRequestsPerSecondForNamespace: 10,
	}
}

// DiscoveryFilters returns filters to exclude certain resources from discovery.
// Excludes system namespaces by default.
func (p *Plugin) DiscoveryFilters() []model.MatchFilter {
	return []model.MatchFilter{
		// Exclude kube-system namespace resources
		{
			ResourceTypes: []string{"K8S::Core::Namespace"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "kube-system"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Core::Namespace"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "kube-public"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Core::Namespace"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "kube-node-lease"},
			},
		},
		// Exclude default ServiceAccount (auto-created per namespace)
		{
			ResourceTypes: []string{"K8S::Core::ServiceAccount"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "default"},
			},
		},
		// Exclude default kubernetes API service
		{
			ResourceTypes: []string{"K8S::Core::Service"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "kubernetes"},
			},
		},
		// Exclude system:* ClusterRoles and ClusterRoleBindings (K8S control plane managed)
		// Uses JSONPath search() for prefix matching via existence check
		{
			ResourceTypes: []string{"K8S::Rbac::ClusterRole"},
			Conditions: []model.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^system:')]`},
			},
		},
		{
			ResourceTypes: []string{"K8S::Rbac::ClusterRoleBinding"},
			Conditions: []model.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^system:')]`},
			},
		},
		// Exclude Jobs created by a CronJob on schedule. Standalone Jobs
		// (no ownerReferences) are still discovered.
		{
			ResourceTypes: []string{"K8S::Batch::Job"},
			Conditions: []model.FilterCondition{
				{PropertyPath: `$.metadata.ownerReferences[?@.kind == "CronJob"]`},
			},
		},
		// Exclude ReplicaSets owned by a Deployment. Standalone ReplicaSets
		// (rare, but valid) are still discovered.
		{
			ResourceTypes: []string{"K8S::Apps::ReplicaSet"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.ownerReferences[0]"},
			},
		},
		// Exclude Pods created by a controller (ReplicaSet, Job, DaemonSet,
		// StatefulSet). Standalone Pods are still discovered.
		{
			ResourceTypes: []string{"K8S::Core::Pod"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.ownerReferences[0]"},
			},
		},
		// Exclude Endpoints objects — K8S maintains one per Service with
		// the Service as owner.
		{
			ResourceTypes: []string{"K8S::Core::Endpoints"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.ownerReferences[0]"},
			},
		},
		// Exclude auto-generated ServiceAccount token Secrets.
		{
			ResourceTypes: []string{"K8S::Core::Secret"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.type", PropertyValue: "kubernetes.io/service-account-token"},
			},
		},
		// Exclude Leases in kube-system — all are control-plane leader
		// election artifacts (kube-controller-manager, kube-scheduler,
		// cloud-controller-manager, node leases, etc.).
		{
			ResourceTypes: []string{"K8S::Coordination::Lease"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.namespace", PropertyValue: "kube-system"},
			},
		},
		// Exclude system-* FlowSchemas (ship with the apiserver).
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^system-')]`},
			},
		},
		// FlowSchema also includes 'exempt' and 'global-default' — match them explicitly.
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "exempt"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "global-default"},
			},
		},
		// Exclude system-* PriorityLevelConfigurations.
		{
			ResourceTypes: []string{"K8S::Flowcontrol::PriorityLevelConfiguration"},
			Conditions: []model.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^system-')]`},
			},
		},
		// Exclude system-* PriorityClasses (system-cluster-critical,
		// system-node-critical — ship with the apiserver).
		{
			ResourceTypes: []string{"K8S::Scheduling::PriorityClass"},
			Conditions: []model.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^system-')]`},
			},
		},
	}
}

// LabelConfig returns the configuration for extracting human-readable labels
// from discovered resources.
//
// We intentionally return an empty LabelConfig so the formae labeler falls
// through to its final branch and uses the NativeID the plugin returned.
// For K8S that NativeID is produced by prov.NativeID and is:
//
//   - Namespaced resources → "<namespace>/<name>"  (e.g. "default/web")
//   - Cluster-scoped       → "<name>"              (e.g. "prod")
//
// Using the NativeID directly avoids the collision that a bare
// "$.metadata.name" query creates when two resources share a name across
// namespaces — the labeler would otherwise label both "web" and append a
// non-deterministic "-N" suffix to whichever was discovered second.
//
// If a resource type ever needs a different label source, add a
// ResourceOverrides entry below.
func (p *Plugin) LabelConfig() model.LabelConfig {
	return model.LabelConfig{}
}

// =============================================================================
// CRUD Operations
// =============================================================================

// getProvisioner creates a provisioner for the given resource type.
// With WrapTransport handling token refresh, clients no longer need
// the TTL-based cache that was previously used for EKS token rotation.
func (p *Plugin) getProvisioner(ctx context.Context, resourceType string, targetConfig []byte) (prov.Provisioner, error) {
	if !registry.HasProvisioner(resourceType) {
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}

	cfg, err := config.FromTargetConfig(targetConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to extract config: %w", err)
	}

	client, err := transport.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create K8S client: %w", err)
	}

	factory, _ := registry.GetFactory(resourceType)
	return factory(client, cfg), nil
}

// Create provisions a new K8S resource.
func (p *Plugin) Create(ctx context.Context, req *resource.CreateRequest) (*resource.CreateResult, error) {
	provisioner, err := p.getProvisioner(ctx, req.ResourceType, req.TargetConfig)
	if err != nil {
		return nil, err
	}
	return provisioner.Create(ctx, req)
}

// Read retrieves the current state of a K8S resource.
func (p *Plugin) Read(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	provisioner, err := p.getProvisioner(ctx, req.ResourceType, req.TargetConfig)
	if err != nil {
		return nil, err
	}
	return provisioner.Read(ctx, req)
}

// Update modifies an existing K8S resource using server-side apply.
func (p *Plugin) Update(ctx context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error) {
	provisioner, err := p.getProvisioner(ctx, req.ResourceType, req.TargetConfig)
	if err != nil {
		return nil, err
	}
	return provisioner.Update(ctx, req)
}

// Delete removes a K8S resource.
func (p *Plugin) Delete(ctx context.Context, req *resource.DeleteRequest) (*resource.DeleteResult, error) {
	provisioner, err := p.getProvisioner(ctx, req.ResourceType, req.TargetConfig)
	if err != nil {
		return nil, err
	}
	return provisioner.Delete(ctx, req)
}

// Status checks the progress of an async operation.
func (p *Plugin) Status(ctx context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	provisioner, err := p.getProvisioner(ctx, req.ResourceType, req.TargetConfig)
	if err != nil {
		return nil, err
	}
	return provisioner.Status(ctx, req)
}

// List returns all resource identifiers of a given type for discovery.
func (p *Plugin) List(ctx context.Context, req *resource.ListRequest) (*resource.ListResult, error) {
	provisioner, err := p.getProvisioner(ctx, req.ResourceType, req.TargetConfig)
	if err != nil {
		return nil, err
	}
	return provisioner.List(ctx, req)
}
