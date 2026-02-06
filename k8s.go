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
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	// Import resources to trigger init() registration
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/apps"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/batch"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/core"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/networking"
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
func (p *Plugin) RateLimit() plugin.RateLimitConfig {
	return plugin.RateLimitConfig{
		Scope:                            plugin.RateLimitScopeNamespace,
		MaxRequestsPerSecondForNamespace: 10,
	}
}

// DiscoveryFilters returns filters to exclude certain resources from discovery.
// Excludes system namespaces by default.
func (p *Plugin) DiscoveryFilters() []plugin.MatchFilter {
	return []plugin.MatchFilter{
		// Exclude kube-system namespace resources
		{
			ResourceTypes: []string{"K8S::Core::Namespace"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "kube-system"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Core::Namespace"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "kube-public"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Core::Namespace"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "kube-node-lease"},
			},
		},
		// Exclude default ServiceAccount (auto-created per namespace)
		{
			ResourceTypes: []string{"K8S::Core::ServiceAccount"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "default"},
			},
		},
		// Exclude default kubernetes API service
		{
			ResourceTypes: []string{"K8S::Core::Service"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "kubernetes"},
			},
		},
	}
}

// LabelConfig returns the configuration for extracting human-readable labels
// from discovered resources. K8S resources use metadata.name.
func (p *Plugin) LabelConfig() plugin.LabelConfig {
	return plugin.LabelConfig{
		DefaultQuery: "$.metadata.name",
	}
}

// =============================================================================
// CRUD Operations
// =============================================================================

// getProvisioner creates or retrieves a provisioner for the given resource type.
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
