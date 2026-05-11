// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"errors"
	"fmt"
	"strings"
)

// NativeID creates a native ID for a namespaced resource in "namespace/name" format.
func NativeID(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// ErrInvalidNativeID is returned when a native ID does not parse correctly.
// Callers can use errors.Is to detect any malformed-id failure mode.
var ErrInvalidNativeID = errors.New("invalid native id")

// ParseNamespacedNativeID parses a native ID for a namespaced resource.
// The format is strictly "namespace/name" with both parts non-empty.
//
// Rejecting malformed input is critical: a silent acceptance of "", "/",
// "/name", "name/", or "a/b/c" yields empty namespace or name, which causes
// downstream client-go Get calls to return 404. The lifecycle decorator then
// surfaces that as OperationErrorCodeNotFound, prompting Formae to drop the
// resource from state — a silent state-loss bug.
func ParseNamespacedNativeID(nativeID string) (namespace, name string, err error) {
	if nativeID == "" {
		return "", "", fmt.Errorf("%w: empty", ErrInvalidNativeID)
	}
	parts := strings.Split(nativeID, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("%w: expected exactly one '/' separator, got %d segments", ErrInvalidNativeID, len(parts))
	}
	if parts[0] == "" {
		return "", "", fmt.Errorf("%w: empty namespace", ErrInvalidNativeID)
	}
	if parts[1] == "" {
		return "", "", fmt.Errorf("%w: empty name", ErrInvalidNativeID)
	}
	return parts[0], parts[1], nil
}

// ParseClusterNativeID parses a native ID for a cluster-scoped resource.
// The format is strictly "name" with no separator and no empty segment.
func ParseClusterNativeID(nativeID string) (name string, err error) {
	if nativeID == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidNativeID)
	}
	if strings.Contains(nativeID, "/") {
		return "", fmt.Errorf("%w: cluster-scoped native id must not contain '/'", ErrInvalidNativeID)
	}
	return nativeID, nil
}

// ParseNativeID splits a native ID into namespace and name.
// For namespaced resources the format is "namespace/name".
// For cluster-scoped resources the format is just "name" (namespace will be empty).
//
// Deprecated: use ParseNamespacedNativeID or ParseClusterNativeID instead.
// This shim silently accepts malformed input which causes downstream state
// loss when the resulting (ns, name) is invalid.
func ParseNativeID(nativeID string) (namespace, name string) {
	parts := strings.SplitN(nativeID, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", parts[0]
}

// NativeIDWithUID creates a native ID that includes the UID for uniqueness.
// Format: "name:uid". Used for cluster-scoped resources where the name alone
// doesn't change across delete+recreate but the UID does.
func NativeIDWithUID(name, uid string) string {
	return fmt.Sprintf("%s:%s", name, uid)
}

// ParseNativeIDWithUID splits a native ID in "name:uid" format.
// Returns (name, uid). If no ":" is present, uid will be empty (backwards compatible
// with old "name-only" native IDs).
func ParseNativeIDWithUID(nativeID string) (name, uid string) {
	parts := strings.SplitN(nativeID, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}
