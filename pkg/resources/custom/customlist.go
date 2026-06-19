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

// List enumerates custom-resource instances by walking the installed
// CustomResourceDefinitions (apiextensions.k8s.io) — never built-in API groups —
// so the catch-all never double-discovers kinds that have typed provisioners.
//
// NOTE: both K8S::Custom::Resource and K8S::Apiextensions::CustomResourceDefinition
// are currently marked discoverable=false in their pkl schemas, so formae does
// NOT invoke this during discovery. The implementation is kept intact, ready to
// re-enable once a scoped discovery design lands (a single catch-all type spans
// every CRD kind, so unscoped discovery would flood inventory with
// operator-internal resources).
func (c *CustomResource) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	crds, err := c.Client.Dynamic.Resource(crdGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list CustomResourceDefinitions: %w", err)
	}

	var nativeIDs []string
	for i := range crds.Items {
		// One served version is enough to enumerate instances (all versions back
		// the same objects).
		group, plural, version := crdServedGVR(&crds.Items[i])
		if group == "" || plural == "" || version == "" {
			continue
		}
		gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: plural}
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

// crdServedGVR extracts the group, plural resource name, and first served
// version from a CustomResourceDefinition object. version is "" if none served.
func crdServedGVR(crd *unstructured.Unstructured) (group, plural, version string) {
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
				return group, plural, name
			}
		}
	}
	return group, plural, ""
}
