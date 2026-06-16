// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package custom

import (
	"context"
	"fmt"
	"strings"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// List enumerates custom-resource instances for the opt-in allowlisted API
// groups (Config.CustomResourceGroups). With an empty allowlist it returns
// nothing, so custom-resource discovery is off by default and a fresh cluster
// does not flood inventory with operator-internal CRs.
func (c *CustomResource) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	if c.Config == nil || len(c.Config.CustomResourceGroups) == 0 {
		return &resource.ListResult{}, nil
	}
	allowed := make(map[string]bool, len(c.Config.CustomResourceGroups))
	for _, g := range c.Config.CustomResourceGroups {
		allowed[g] = true
	}

	disc := c.Client.Discovery()
	_, apiResourceLists, err := disc.ServerGroupsAndResources()
	if err != nil {
		// Partial discovery errors are common (aggregated/stale APIs); proceed
		// with whatever resolved rather than failing the whole discovery pass.
		if apiResourceLists == nil {
			return nil, fmt.Errorf("discover server resources: %w", err)
		}
	}

	var nativeIDs []string
	for _, rl := range apiResourceLists {
		gv, err := schema.ParseGroupVersion(rl.GroupVersion)
		if err != nil || !allowed[gv.Group] {
			continue
		}
		for _, r := range rl.APIResources {
			if !canList(r) {
				continue
			}
			gvr := gv.WithResource(r.Name)
			page, err := c.Client.Dynamic.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				continue // listing one kind failing must not abort the rest
			}
			for i := range page.Items {
				item := &page.Items[i]
				nativeIDs = append(nativeIDs, prov.CustomResourceID(
					item.GetAPIVersion(), item.GetKind(), item.GetNamespace(), item.GetName(),
				))
			}
		}
	}
	return &resource.ListResult{NativeIDs: nativeIDs}, nil
}

// canList reports whether an APIResource is a primary, listable kind (not a
// subresource like "certificates/status") and supports the list verb.
func canList(r metav1.APIResource) bool {
	if r.Name == "" || strings.Contains(r.Name, "/") {
		return false
	}
	for _, v := range r.Verbs {
		if v == "list" {
			return true
		}
	}
	return false
}
