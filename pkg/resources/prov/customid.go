// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"fmt"
	"strings"
)

// CustomResourceID builds the canonical identity string for a custom resource:
//
//	<apiVersion>/<kind>/<namespace>/<name>
//
// namespace is empty for cluster-scoped objects. This single string is used
// both as the formae identity field ("$.formaeId" in the catch-all pkl class)
// and as the plugin NativeID, so Read/Update/Delete/Status can recover the GVK
// without a per-kind type. apiVersion may contain one '/' (group/version);
// kind, namespace, and name never contain '/'.
func CustomResourceID(apiVersion, kind, namespace, name string) string {
	return fmt.Sprintf("%s/%s/%s/%s", apiVersion, kind, namespace, name)
}

// ParseCustomResourceID splits a canonical custom-resource ID back into its
// parts. It splits the last three '/'-separated segments as name, namespace,
// kind and rejoins the remainder as apiVersion. name and apiVersion must be
// non-empty; namespace may be empty (cluster-scoped). kind must be non-empty.
func ParseCustomResourceID(id string) (apiVersion, kind, namespace, name string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) < 4 {
		return "", "", "", "", fmt.Errorf("%w: expected <apiVersion>/<kind>/<namespace>/<name>, got %q", ErrInvalidNativeID, id)
	}
	name = parts[len(parts)-1]
	namespace = parts[len(parts)-2]
	kind = parts[len(parts)-3]
	apiVersion = strings.Join(parts[:len(parts)-3], "/")
	if apiVersion == "" || kind == "" || name == "" {
		return "", "", "", "", fmt.Errorf("%w: empty apiVersion/kind/name in %q", ErrInvalidNativeID, id)
	}
	return apiVersion, kind, namespace, name, nil
}
