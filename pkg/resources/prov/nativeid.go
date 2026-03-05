// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"fmt"
	"strings"
)

// NativeID creates a native ID for a namespaced resource in "namespace/name" format.
func NativeID(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// ParseNativeID splits a native ID into namespace and name.
// For namespaced resources the format is "namespace/name".
// For cluster-scoped resources the format is just "name" (namespace will be empty).
func ParseNativeID(nativeID string) (namespace, name string) {
	parts := strings.SplitN(nativeID, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", parts[0]
}
