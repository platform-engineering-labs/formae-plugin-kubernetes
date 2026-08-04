//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"

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
