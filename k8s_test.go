//go:build unit

// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findFilter returns the first MatchFilter that applies to resourceType and
// whose first condition's PropertyPath contains pathSubstring. The substring
// match lets tests assert intent (e.g. "a filter on ownerReferences for Pods
// exists") without hard-coding exact JSONPath syntax, which may evolve.
func findFilter(t *testing.T, filters []model.MatchFilter, resourceType, pathSubstring string) model.MatchFilter {
	t.Helper()
	for _, f := range filters {
		if len(f.Conditions) == 0 {
			continue
		}
		hasType := false
		for _, rt := range f.ResourceTypes {
			if rt == resourceType {
				hasType = true
				break
			}
		}
		if !hasType {
			continue
		}
		if pathSubstring == "" || strings.Contains(f.Conditions[0].PropertyPath, pathSubstring) {
			return f
		}
	}
	t.Fatalf("no filter found for resourceType=%q containing path substring %q", resourceType, pathSubstring)
	return model.MatchFilter{}
}

func TestDiscoveryFilters_CronJobOwnedJobs(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Batch::Job", "ownerReferences")
	require.Len(t, f.Conditions, 1, "filter should have exactly one condition")
	assert.Contains(t, f.Conditions[0].PropertyPath, "CronJob",
		"condition should match owner references of kind CronJob")
}

func TestDiscoveryFilters_OwnedReplicaSets(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Apps::ReplicaSet", "ownerReferences")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "ownerReferences")
}

func TestDiscoveryFilters_OwnedPods(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Core::Pod", "ownerReferences")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "ownerReferences")
}

func TestDiscoveryFilters_OwnedEndpoints(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Core::Endpoints", "ownerReferences")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "ownerReferences")
}

func TestDiscoveryFilters_ServiceAccountTokenSecrets(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Core::Secret", "type")
	require.Len(t, f.Conditions, 1)
	assert.Equal(t, "$.type", f.Conditions[0].PropertyPath)
	assert.Equal(t, "kubernetes.io/service-account-token", f.Conditions[0].PropertyValue)
}

func TestDiscoveryFilters_KubeSystemLeases(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Coordination::Lease", "namespace")
	require.Len(t, f.Conditions, 1)
	assert.Equal(t, "$.metadata.namespace", f.Conditions[0].PropertyPath)
	assert.Equal(t, "kube-system", f.Conditions[0].PropertyValue)
}

func TestDiscoveryFilters_SystemFlowSchemas(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Flowcontrol::FlowSchema", "system-")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "system-")
}

func TestDiscoveryFilters_SystemPriorityLevelConfigurations(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Flowcontrol::PriorityLevelConfiguration", "system-")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "system-")
}

func TestDiscoveryFilters_SystemPriorityClasses(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Scheduling::PriorityClass", "system-")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "system-")
}

// hasNameFilter reports whether the filter set contains at least one
// MatchFilter whose ResourceTypes include resourceType and whose first
// condition matches metadata.name == name.
func hasNameFilter(filters []model.MatchFilter, resourceType, name string) bool {
	for _, f := range filters {
		hasType := false
		for _, rt := range f.ResourceTypes {
			if rt == resourceType {
				hasType = true
				break
			}
		}
		if !hasType || len(f.Conditions) == 0 {
			continue
		}
		c := f.Conditions[0]
		if c.PropertyPath == "$.metadata.name" && c.PropertyValue == name {
			return true
		}
	}
	return false
}

// hasMetadataSearchFilter reports whether the filter set contains a
// MatchFilter on resourceType using a JSONPath search() expression that
// contains the given pattern substring (e.g. "^system-", "^eks-").
func hasMetadataSearchFilter(filters []model.MatchFilter, resourceType, patternSubstring string) bool {
	for _, f := range filters {
		hasType := false
		for _, rt := range f.ResourceTypes {
			if rt == resourceType {
				hasType = true
				break
			}
		}
		if !hasType || len(f.Conditions) == 0 {
			continue
		}
		p := f.Conditions[0].PropertyPath
		if strings.Contains(p, "search(") && strings.Contains(p, patternSubstring) {
			return true
		}
	}
	return false
}

// TestDiscoveryFilters_BuiltIns asserts that the plugin-level
// DiscoveryFilters() excludes the system-installed cluster resources
// called out in the pre-release review (H-DSC-1): system PriorityClasses,
// the bootstrap FlowSchemas / PriorityLevelConfigurations, the cloud-
// provider default StorageClasses, the cloud-provider admission webhooks,
// and the system: RBAC ClusterRoles/ClusterRoleBindings (moved here from
// List-level filtering in pkg/resources/rbac/).
func TestDiscoveryFilters_BuiltIns(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	t.Run("PriorityClass", func(t *testing.T) {
		assert.True(t, hasMetadataSearchFilter(filters, "K8S::Scheduling::PriorityClass", "^system-"),
			"PriorityClass should be filtered by system- prefix")
	})

	t.Run("FlowSchema system- prefix", func(t *testing.T) {
		assert.True(t, hasMetadataSearchFilter(filters, "K8S::Flowcontrol::FlowSchema", "^system-"))
	})

	t.Run("FlowSchema bootstrap names", func(t *testing.T) {
		// All of these ship with kube-apiserver and are managed by the control plane.
		for _, name := range []string{
			"exempt",
			"global-default",
			"catch-all",
			"probes",
			"service-accounts",
			"kube-controller-manager",
			"kube-scheduler",
			"endpoint-controller",
			"workload-high",
			"workload-low",
		} {
			assert.True(t, hasNameFilter(filters, "K8S::Flowcontrol::FlowSchema", name),
				"FlowSchema %q should be filtered", name)
		}
	})

	t.Run("PriorityLevelConfiguration system- prefix", func(t *testing.T) {
		assert.True(t, hasMetadataSearchFilter(filters, "K8S::Flowcontrol::PriorityLevelConfiguration", "^system-"))
	})

	t.Run("PriorityLevelConfiguration bootstrap names", func(t *testing.T) {
		for _, name := range []string{
			"catch-all",
			"exempt",
			"workload-high",
			"workload-low",
			"node-high",
			"leader-election",
			"global-default",
		} {
			assert.True(t, hasNameFilter(filters, "K8S::Flowcontrol::PriorityLevelConfiguration", name),
				"PriorityLevelConfiguration %q should be filtered", name)
		}
	})

	t.Run("StorageClass cloud defaults", func(t *testing.T) {
		// One representative name per managed K8s distribution.
		for _, name := range []string{
			"gp2",          // EKS in-tree
			"gp3",          // EKS EBS CSI
			"standard",     // GKE
			"standard-rwo", // GKE regional
			"premium-rwo",  // GKE premium
			"default",      // AKS
			"managed-premium",
			"local-path", // KinD / k3s
			"orbstack",   // OrbStack
		} {
			assert.True(t, hasNameFilter(filters, "K8S::Storage::StorageClass", name),
				"StorageClass %q should be filtered", name)
		}
	})

	t.Run("Admission webhook configs cloud-provider prefixes", func(t *testing.T) {
		for _, rt := range []string{
			"K8S::Admissionregistration::MutatingWebhookConfiguration",
			"K8S::Admissionregistration::ValidatingWebhookConfiguration",
		} {
			for _, prefix := range []string{"^eks-", "^gke-", "^aks-"} {
				assert.True(t, hasMetadataSearchFilter(filters, rt, prefix),
					"%s should be filtered by %q", rt, prefix)
			}
		}
	})

	t.Run("RBAC system: ClusterRoles + ClusterRoleBindings", func(t *testing.T) {
		// Moved from List-level filtering in pkg/resources/rbac/*.go into
		// plugin-level DiscoveryFilters for consistency with the other
		// system-resource exclusions. See H-DSC-1.
		assert.True(t, hasMetadataSearchFilter(filters, "K8S::Rbac::ClusterRole", "^system:"),
			"ClusterRole should be filtered by ^system: prefix at plugin level")
		assert.True(t, hasMetadataSearchFilter(filters, "K8S::Rbac::ClusterRoleBinding", "^system:"),
			"ClusterRoleBinding should be filtered by ^system: prefix at plugin level")
	})
}
