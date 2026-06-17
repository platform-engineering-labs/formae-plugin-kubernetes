// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package custom

import (
	"context"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// List enumerates custom-resource instances for discovery, the same way the
// typed provisioners enumerate their kinds. Candidate kinds are sourced from
// the installed CustomResourceDefinitions (apiextensions.k8s.io) — never from
// built-in API groups — so the catch-all never double-discovers kinds that
// already have their own typed provisioners.
//
// As with the built-in types, this returns everything; operator-internal or
// otherwise unwanted kinds are excluded via the plugin's DiscoveryFilters
// (Plugin.DiscoveryFilters), not a bespoke per-target config.
func (c *CustomResource) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	crds, err := c.Client.Dynamic.Resource(crdGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list CustomResourceDefinitions: %w", err)
	}

	var nativeIDs []string
	for i := range crds.Items {
		group, plural, versions := crdServedGVR(&crds.Items[i])
		if group == "" || plural == "" || len(versions) == 0 {
			continue
		}
		// One served version is enough to enumerate instances (all versions back
		// the same objects); use the first served version.
		gvr := schema.GroupVersionResource{Group: group, Version: versions[0], Resource: plural}
		page, err := c.Client.Dynamic.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue // a single kind failing to list must not abort the whole pass
		}
		for j := range page.Items {
			item := &page.Items[j]
			nativeIDs = append(nativeIDs, prov.CustomResourceID(
				item.GetAPIVersion(), item.GetKind(), item.GetNamespace(), item.GetName(),
			))
		}
	}
	return &resource.ListResult{NativeIDs: nativeIDs}, nil
}

// crdServedGVR extracts the group, plural resource name, and served version
// names from a CustomResourceDefinition object.
func crdServedGVR(crd *unstructured.Unstructured) (group, plural string, servedVersions []string) {
	group, _, _ = unstructured.NestedString(crd.Object, "spec", "group")
	plural, _, _ = unstructured.NestedString(crd.Object, "spec", "names", "plural")
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	for _, v := range versions {
		vm, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if served, _ := vm["served"].(bool); served {
			if name, _ := vm["name"].(string); name != "" {
				servedVersions = append(servedVersions, name)
			}
		}
	}
	return group, plural, servedVersions
}
