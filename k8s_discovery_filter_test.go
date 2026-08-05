//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"strings"
	"testing"

	k8sregistry "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"

	"github.com/platform-engineering-labs/formae/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theory/jsonpath"
	"github.com/theory/jsonpath/registry"
)

// The filter tests in k8s_test.go assert the *shape* of a MatchFilter — that a
// condition exists and mentions ownerReferences. That cannot catch a filter
// whose JSONPath selects nothing, and one of them was passing against exactly
// that: `$.metadata.ownerReferences[0]` on K8S::Core::Endpoints never matches,
// because the endpoints controller does not set ownerReferences at all.
//
// So these tests evaluate the filters the way discovery does, against real
// object JSON captured from a 1.33 cluster.

var filterParser = jsonpath.NewParser(jsonpath.WithRegistry(registry.New()))

// excludes reports whether a MatchFilter would drop this object from discovery.
//
// Mirrors model's semantics: every condition must match (AND), a condition with
// no PropertyValue matches when its path selects anything at all, and one with a
// PropertyValue matches when the selected value equals it.
func excludes(t *testing.T, f model.MatchFilter, objectJSON string) bool {
	t.Helper()
	var data any
	require.NoError(t, json.Unmarshal([]byte(objectJSON), &data))

	for _, cond := range f.Conditions {
		path, err := filterParser.Parse(cond.PropertyPath)
		require.NoErrorf(t, err, "unparseable filter path %q", cond.PropertyPath)
		nodes := path.Select(data)
		if len(nodes) == 0 {
			return false
		}
		if cond.PropertyValue != "" {
			got, ok := nodes[0].(string)
			if !ok || got != cond.PropertyValue {
				return false
			}
		}
	}
	return len(f.Conditions) > 0
}

// endpointsFor returns every filter that applies to K8S::Core::Endpoints.
func endpointsFilters(t *testing.T) []model.MatchFilter {
	t.Helper()
	var out []model.MatchFilter
	for _, f := range (&Plugin{}).DiscoveryFilters() {
		for _, rt := range f.ResourceTypes {
			if rt == "K8S::Core::Endpoints" {
				out = append(out, f)
				break
			}
		}
	}
	require.NotEmpty(t, out, "no Endpoints filters at all")
	return out
}

func excludedByAny(t *testing.T, filters []model.MatchFilter, objectJSON string) bool {
	t.Helper()
	for _, f := range filters {
		if excludes(t, f, objectJSON) {
			return true
		}
	}
	return false
}

// A Service's Endpoints, as the endpoints controller writes it. Captured from a
// 1.33 cluster: no ownerReferences, and the controller's own managed-by label.
const controllerManagedEndpoints = `{
  "apiVersion": "v1",
  "kind": "Endpoints",
  "metadata": {
    "name": "app",
    "namespace": "hh-ep",
    "labels": {
      "app": "app",
      "endpoints.kubernetes.io/managed-by": "endpoint-controller"
    },
    "annotations": {
      "endpoints.kubernetes.io/last-change-trigger-time": "2026-08-04T13:01:58Z"
    }
  }
}`

// An Endpoints with no Service of the same name. The controller never touches
// it, so it carries no labels — this is somebody's deliberate resource and has
// to stay discoverable.
const standaloneEndpoints = `{
  "apiVersion": "v1",
  "kind": "Endpoints",
  "metadata": {
    "name": "orphan-ep",
    "namespace": "hh-ep"
  },
  "subsets": [{"addresses": [{"ip": "10.1.2.4"}], "ports": [{"port": 80}]}]
}`

// The apiserver-maintained default Endpoints. Not the endpoints controller's, so
// it has no managed-by label — which is why it needs its own filter.
const defaultKubernetesEndpoints = `{
  "apiVersion": "v1",
  "kind": "Endpoints",
  "metadata": {
    "name": "kubernetes",
    "namespace": "default",
    "labels": {"endpointslice.kubernetes.io/skip-mirror": "true"}
  }
}`

// A chart's Service produces one of these for every Service it renders, and it
// is not in the release manifest, so the Helm collapse cannot hide it either.
// If discovery does not filter it, every charted Service leaks an unmanaged row.
func TestDiscoveryFilters_ExcludesControllerManagedEndpoints(t *testing.T) {
	assert.True(t, excludedByAny(t, endpointsFilters(t), controllerManagedEndpoints),
		"an Endpoints the endpoints controller manages for a Service must be filtered; "+
			"it is derived state, not somebody's resource")
}

func TestDiscoveryFilters_KeepsStandaloneEndpoints(t *testing.T) {
	assert.False(t, excludedByAny(t, endpointsFilters(t), standaloneEndpoints),
		"an Endpoints with no matching Service is user-authored and must stay discoverable")
}

func TestDiscoveryFilters_ExcludesDefaultKubernetesEndpoints(t *testing.T) {
	assert.True(t, excludedByAny(t, endpointsFilters(t), defaultKubernetesEndpoints),
		"the apiserver's default/kubernetes Endpoints must be filtered")
}

// A cluster-scoped object Helm applied for a release. Captured from a real
// cluster: formae stores dotted annotation keys in BOTH forms, flat and expanded
// into nested maps, so the filter has to read the flat one.
const helmAppliedClusterRoleBinding = `{
  "apiVersion": "rbac.authorization.k8s.io/v1",
  "kind": "ClusterRoleBinding",
  "metadata": {
    "name": "kube-prom-kube-prometheus-operator",
    "annotations": {
      "meta": {"helm": {"sh/release-name": "kube-prom", "sh/release-namespace": "monitoring"}},
      "meta.helm.sh/release-name": "kube-prom",
      "meta.helm.sh/release-namespace": "monitoring"
    },
    "labels": {
      "app.kubernetes.io/managed-by": "Helm",
      "heritage": "Helm",
      "release": "kube-prom"
    }
  }
}`

// Somebody's own ClusterRoleBinding. No Helm annotations, so it must survive.
const handMadeClusterRoleBinding = `{
  "apiVersion": "rbac.authorization.k8s.io/v1",
  "kind": "ClusterRoleBinding",
  "metadata": {"name": "my-own-binding"}
}`

func filtersFor(t *testing.T, resourceType string) []model.MatchFilter {
	t.Helper()
	var out []model.MatchFilter
	for _, f := range (&Plugin{}).DiscoveryFilters() {
		for _, rt := range f.ResourceTypes {
			if rt == resourceType {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// Anything Helm applied carries meta.helm.sh/release-name, and the
// K8S::Helm::Release that owns it stands in for it. The manifest-based collapse
// in collapseHelmOwned covers this too, but only while the release inventory can
// be built — this filter is static, needs no apiserver call, and still holds when
// that build fails.
func TestDiscoveryFilters_ExcludesHelmAppliedObjects(t *testing.T) {
	assert.True(t,
		excludedByAny(t, filtersFor(t, "K8S::Rbac::ClusterRoleBinding"), helmAppliedClusterRoleBinding),
		"an object carrying meta.helm.sh/release-name was applied by Helm and belongs to "+
			"a release, so it must not surface as an unmanaged resource of its own")
}

func TestDiscoveryFilters_KeepsHandMadeClusterRoleBinding(t *testing.T) {
	assert.False(t,
		excludedByAny(t, filtersFor(t, "K8S::Rbac::ClusterRoleBinding"), handMadeClusterRoleBinding),
		"a ClusterRoleBinding with no Helm annotations is somebody's own resource")
}

// The Helm-applied filter is only worth anything if it covers every type a chart
// can render, so a missing entry is a silent hole rather than a visible error.
func TestDiscoveryFilters_HelmAppliedCoversEveryDiscoverableType(t *testing.T) {
	for _, rt := range k8sregistry.ResourceTypes() {
		// Two deliberate exceptions, each with its own test above: the release is
		// what the collapse exists to surface, and a Namespace is the discovery
		// parent for every namespaced type — filtering one hides everything
		// inside it.
		if rt == "K8S::Helm::Release" || rt == "K8S::Core::Namespace" ||
			strings.HasPrefix(rt, "K8S::Test::") {
			continue
		}
		assert.Truef(t, excludedByAny(t, filtersFor(t, rt), helmAppliedClusterRoleBinding),
			"%s has no Helm-applied filter, so Helm-owned objects of that type leak", rt)
	}
}

// A Namespace a chart rendered. Helm stamps it exactly like any other object, so
// the Helm-applied filter would hide it — and hiding a Namespace is not a
// one-row decision.
const helmAppliedNamespace = `{
  "apiVersion": "v1",
  "kind": "Namespace",
  "metadata": {
    "name": "hh-tmplns",
    "annotations": {
      "meta.helm.sh/release-name": "nsc",
      "meta.helm.sh/release-namespace": "default"
    }
  }
}`

// Never filter a Namespace on Helm ownership.
//
// Every namespaced type declares parent = K8S::Core::Namespace and discovery
// walks children per *discovered* namespace, so excluding a Namespace removes
// everything inside it from discovery — including objects Helm never touched, and
// including any K8S::Helm::Release installed there. Measured on a chart that
// templates its own namespace: a hand-made ConfigMap in it disappeared from
// discovery entirely.
//
// Showing one extra unmanaged Namespace row is the cheaper mistake by a wide
// margin.
func TestDiscoveryFilters_NeverFiltersANamespaceOnHelmOwnership(t *testing.T) {
	assert.False(t,
		excludedByAny(t, filtersFor(t, "K8S::Core::Namespace"), helmAppliedNamespace),
		"a Namespace is a discovery parent: filtering it hides every resource "+
			"inside it, Helm's or not")
}

// The release itself must never be filtered by its own ownership annotations —
// it is the resource the collapse exists to surface.
func TestDiscoveryFilters_KeepsTheReleaseItself(t *testing.T) {
	assert.False(t,
		excludedByAny(t, filtersFor(t, "K8S::Helm::Release"), helmAppliedClusterRoleBinding),
		"K8S::Helm::Release must stay discoverable")
}

// Guards the specific mistake this file exists for: a filter that cannot fire.
// An Endpoints filter keyed on ownerReferences is inert, because no Endpoints
// object has any, so it would silently cover nothing.
func TestDiscoveryFilters_NoInertOwnerReferencesEndpointsFilter(t *testing.T) {
	for _, f := range endpointsFilters(t) {
		for _, cond := range f.Conditions {
			assert.NotContains(t, cond.PropertyPath, "ownerReferences",
				"Endpoints carry no ownerReferences, so a filter on them never matches; "+
					"key on the endpoints.kubernetes.io/managed-by label instead")
		}
	}
}
