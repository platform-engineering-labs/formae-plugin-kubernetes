// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/model"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	// Import resources to trigger init() registration
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/admissionregistration"
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
		// Exclude additional bootstrap FlowSchemas that don't carry the
		// 'system-' prefix but are installed and managed by kube-apiserver.
		// An operator who wants to manage one of these explicitly can drop
		// the corresponding filter entry below.
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "catch-all"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "probes"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "service-accounts"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "kube-controller-manager"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "kube-scheduler"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "endpoint-controller"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "workload-high"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "workload-low"},
			},
		},
		// Exclude additional bootstrap PriorityLevelConfigurations without
		// the 'system-' prefix (catch-all/exempt are bootstrap, workload-low
		// is bootstrap on older versions; same removal recipe as above).
		{
			ResourceTypes: []string{"K8S::Flowcontrol::PriorityLevelConfiguration"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "catch-all"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::PriorityLevelConfiguration"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "exempt"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::PriorityLevelConfiguration"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "workload-high"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::PriorityLevelConfiguration"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "workload-low"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::PriorityLevelConfiguration"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "node-high"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::PriorityLevelConfiguration"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "leader-election"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::PriorityLevelConfiguration"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "global-default"},
			},
		},
		// Exclude cloud-installed default StorageClasses. Each managed K8s
		// distribution installs its own default — we exclude the well-known
		// names so a fresh cluster on EKS/GKE/AKS/KinD/OrbStack doesn't drag
		// the platform-provided StorageClass into discovery. An operator
		// who genuinely wants to manage one (e.g. to mutate parameters)
		// can drop the matching entry.
		{
			// EKS default (gp2-backed in-tree, gp3 via EBS CSI on newer clusters).
			ResourceTypes: []string{"K8S::Storage::StorageClass"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "gp2"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Storage::StorageClass"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "gp3"},
			},
		},
		{
			// GKE default.
			ResourceTypes: []string{"K8S::Storage::StorageClass"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "standard"},
			},
		},
		{
			// GKE Premium / GKE pd-balanced regional defaults.
			ResourceTypes: []string{"K8S::Storage::StorageClass"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "standard-rwo"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Storage::StorageClass"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "premium-rwo"},
			},
		},
		{
			// AKS default.
			ResourceTypes: []string{"K8S::Storage::StorageClass"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "default"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Storage::StorageClass"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "managed-premium"},
			},
		},
		{
			// KinD / k3s default.
			ResourceTypes: []string{"K8S::Storage::StorageClass"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "local-path"},
			},
		},
		{
			// OrbStack default (hostpath provisioner).
			ResourceTypes: []string{"K8S::Storage::StorageClass"},
			Conditions: []model.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "orbstack"},
			},
		},
		// Exclude managed-provider webhook configurations. EKS, GKE, AKS
		// install MutatingWebhookConfigurations and ValidatingWebhookConfigurations
		// that the platform owns and rotates — pulling them into discovery
		// is noise, and attempting to manage them would conflict with the
		// platform reconciler. We match by well-known prefixes/suffixes used
		// by the cloud providers.
		{
			// e.g. eks-pod-identity-webhook, eks-validating-webhook
			ResourceTypes: []string{
				"K8S::Admissionregistration::MutatingWebhookConfiguration",
				"K8S::Admissionregistration::ValidatingWebhookConfiguration",
			},
			Conditions: []model.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^eks-')]`},
			},
		},
		{
			// e.g. gke-default-snat-webhook, gmp-operator on GKE.
			ResourceTypes: []string{
				"K8S::Admissionregistration::MutatingWebhookConfiguration",
				"K8S::Admissionregistration::ValidatingWebhookConfiguration",
			},
			Conditions: []model.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^gke-')]`},
			},
		},
		{
			// e.g. aks-node-validating-webhook, aks-webhook-admission-controller.
			ResourceTypes: []string{
				"K8S::Admissionregistration::MutatingWebhookConfiguration",
				"K8S::Admissionregistration::ValidatingWebhookConfiguration",
			},
			Conditions: []model.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^aks-')]`},
			},
		},
		// Move RBAC system filters out of List-level into plugin-level
		// DiscoveryFilters so operators can introspect/disable them
		// consistently with other system-resource filters. Matches
		// ClusterRoles and ClusterRoleBindings whose name starts with
		// 'system:' (the kube-apiserver bootstrap convention).
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
	logger := plugin.LoggerFromContext(ctx)
	logger.Info("[K8S-READ] start", "type", req.ResourceType, "nativeID", req.NativeID)
	startTime := time.Now()
	provisioner, err := p.getProvisioner(ctx, req.ResourceType, req.TargetConfig)
	if err != nil {
		logger.Error("[K8S-READ] getProvisioner failed", "type", req.ResourceType, "duration", time.Since(startTime), "error", err)
		return nil, err
	}
	result, err := provisioner.Read(ctx, req)
	duration := time.Since(startTime)
	if err != nil {
		logger.Error("[K8S-READ] failed", "type", req.ResourceType, "nativeID", req.NativeID, "duration", duration, "error", err)
	} else if result.ErrorCode != "" {
		logger.Warn("[K8S-READ] completed with error code", "type", req.ResourceType, "nativeID", req.NativeID, "duration", duration, "errorCode", result.ErrorCode)
	} else {
		logger.Info("[K8S-READ] completed", "type", req.ResourceType, "nativeID", req.NativeID, "duration", duration, "propsLen", len(result.Properties))
	}
	return result, err
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
