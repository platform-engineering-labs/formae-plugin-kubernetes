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
