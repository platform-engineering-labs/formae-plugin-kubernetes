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

// discovery modes for custom resources (Config.CustomResourceDiscovery).
const (
	discoveryNone   = "none"
	discoveryGroups = "groups"
	discoveryAll    = "all"
)

// discoveryMode resolves the effective discovery mode, defaulting to "none" and
// treating a bare (legacy) allowlist as "groups" for backward compatibility.
func (c *CustomResource) discoveryMode() string {
	if c.Config == nil {
		return discoveryNone
	}
	switch c.Config.CustomResourceDiscovery {
	case discoveryAll, discoveryGroups, discoveryNone:
		return c.Config.CustomResourceDiscovery
	case "":
		if len(c.Config.CustomResourceGroups) > 0 {
			return discoveryGroups
		}
		return discoveryNone
	default:
		return discoveryNone
	}
}

// List enumerates custom-resource instances per the configured discovery mode.
// Candidate kinds are sourced from the installed CustomResourceDefinitions
// (apiextensions.k8s.io) — never from built-in API groups — so the catch-all
// never double-discovers kinds that have their own typed provisioners.
//
//   none   — return nothing (default; no inventory flooding).
//   groups — only CRDs whose spec.group is in Config.CustomResourceGroups.
//   all    — every installed CRD.
func (c *CustomResource) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	mode := c.discoveryMode()
	if mode == discoveryNone {
		return &resource.ListResult{}, nil
	}

	allowed := make(map[string]bool, len(c.Config.CustomResourceGroups))
	for _, g := range c.Config.CustomResourceGroups {
		allowed[g] = true
	}

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
		if mode == discoveryGroups && !allowed[group] {
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
