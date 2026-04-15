// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
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

// clientCacheTTL is how long a cached K8S client is reused before being
// recreated. Keeps EKS STS tokens fresh (60s expiry) while avoiding a
// new TLS handshake on every CRUD call.
const clientCacheTTL = 50 * time.Second

type cachedClient struct {
	client    *transport.Client
	createdAt time.Time
}

// Plugin implements the Formae ResourcePlugin interface for Kubernetes.
// The SDK automatically provides identity methods (Name, Version, Namespace)
// by reading formae-plugin.pkl at startup.
type Plugin struct {
	mu      sync.Mutex
	clients map[string]*cachedClient
}

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
	}
}

// LabelConfig returns the configuration for extracting human-readable labels
// from discovered resources. K8S resources use metadata.name.
func (p *Plugin) LabelConfig() model.LabelConfig {
	return model.LabelConfig{
		DefaultQuery: "$.metadata.name",
	}
}

// =============================================================================
// CRUD Operations
// =============================================================================

// getProvisioner creates or retrieves a provisioner for the given resource type.
// Clients are cached by target config hash to avoid a new TLS handshake per call.
func (p *Plugin) getProvisioner(ctx context.Context, resourceType string, targetConfig []byte) (prov.Provisioner, error) {
	if !registry.HasProvisioner(resourceType) {
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}

	cfg, err := config.FromTargetConfig(targetConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to extract config: %w", err)
	}

	client, err := p.getOrCreateClient(cfg, targetConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create K8S client: %w", err)
	}

	factory, _ := registry.GetFactory(resourceType)
	return factory(client, cfg), nil
}

// getOrCreateClient returns a cached client for the given target config,
// or creates a new one if the cache is empty or expired.
func (p *Plugin) getOrCreateClient(cfg *config.Config, targetConfig []byte) (*transport.Client, error) {
	key := fmt.Sprintf("%x", sha256.Sum256(targetConfig))

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.clients == nil {
		p.clients = make(map[string]*cachedClient)
	}

	if cached, ok := p.clients[key]; ok {
		if time.Since(cached.createdAt) < clientCacheTTL {
			return cached.client, nil
		}
		delete(p.clients, key)
	}

	client, err := transport.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	p.clients[key] = &cachedClient{
		client:    client,
		createdAt: time.Now(),
	}
	return client, nil
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
