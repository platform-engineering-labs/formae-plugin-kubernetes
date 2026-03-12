// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FieldManager is the SSA field manager name used by all Formae operations.
const FieldManager = "formae"

// ExtractFunc is the signature of client-go SSA Extract functions.
// Example: appsv1ac.ExtractDeployment, v1coreac.ExtractConfigMap
type ExtractFunc[API any, AC any] func(*API, string) (*AC, error)

// ExtractState uses SSA Extract to return only fields owned by FieldManager as JSON.
// This returns exactly the fields Formae applied — no server defaults, no controller
// injected fields, no server-managed metadata.
func ExtractState[API any, AC any](apiObject *API, extractFn ExtractFunc[API, AC]) ([]byte, error) {
	extracted, err := extractFn(apiObject, FieldManager)
	if err != nil {
		return nil, fmt.Errorf("failed to extract field manager state: %w", err)
	}
	return json.Marshal(extracted)
}

// serverManagedMetaFields are metadata fields set by the K8S API server
// that should not appear in LiveState properties. These cause false drift
// detection because PKL-evaluated properties don't include them.
var serverManagedMetaFields = []string{
	"uid",
	"resourceVersion",
	"creationTimestamp",
	"generation",
	"deletionTimestamp",
	"deletionGracePeriodSeconds",
}

// serverManagedAnnotationPrefixes are annotation key prefixes set by K8S
// controllers asynchronously after resource creation. These must be stripped
// because they may not be present in the Apply result but appear on later
// Get calls, causing false property drift.
var serverManagedAnnotationPrefixes = []string{
	"deployment.kubernetes.io/",
}

// serverManagedLabelPrefixes are label key prefixes set by K8S admission
// controllers. These must be stripped because they cause Formae dot-expansion
// issues (dotted keys get expanded into nested objects) and are not user-managed.
var serverManagedLabelPrefixes = []string{
	"kubernetes.io/",
}

// serviceAccountMountPrefix is the mountPath prefix for auto-injected
// service account token volume mounts.
const serviceAccountMountPrefix = "/var/run/secrets/kubernetes.io/serviceaccount"

// LiveState returns the full live state of a K8S API object as JSON bytes,
// filtered through the typed apply configuration type T with server-managed
// fields stripped.
//
// kind and apiVersion are injected into the result because client-go's typed
// clients do not populate TypeMeta on returned objects.
//
// The apply config round-trip removes most server-only fields, but
// ObjectMetaApplyConfiguration still includes uid, resourceVersion,
// creationTimestamp, generation, etc. We strip these explicitly to prevent
// false "Resource properties changed" detections in Formae.
//
// Additionally strips K8S server defaults that leak through apply config types:
//   - status (entirely server-managed)
//   - empty objects {} (e.g. resources:{}, objectSelector:{})
//   - imagePullPolicy on containers (K8S-defaulted based on image tag)
//   - auto-injected service account volumeMounts
func LiveState[T any](apiObject any, kind, apiVersion string) ([]byte, error) {
	raw, err := json.Marshal(apiObject)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal api object: %w", err)
	}

	var applyConfig T
	if err := json.Unmarshal(raw, &applyConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal into apply configuration: %w", err)
	}

	intermediate, err := json.Marshal(applyConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal apply configuration: %w", err)
	}

	// Strip server-managed fields that leak through the apply config types
	var result map[string]interface{}
	if err := json.Unmarshal(intermediate, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal for field stripping: %w", err)
	}

	delete(result, "status")

	// Inject kind and apiVersion — client-go typed clients strip TypeMeta
	// from returned objects, but Formae requires these as required fields.
	result["kind"] = kind
	result["apiVersion"] = apiVersion

	if meta, ok := result["metadata"].(map[string]interface{}); ok {
		for _, key := range serverManagedMetaFields {
			delete(meta, key)
		}
		stripServerManagedAnnotations(meta)
		stripServerManagedLabels(meta)
	}

	// Strip server-managed spec.finalizers (set by K8S controllers, not user-managed).
	if spec, ok := result["spec"].(map[string]interface{}); ok {
		delete(spec, "finalizers")
		if len(spec) == 0 {
			delete(result, "spec")
		}
	}

	// Recursively strip server defaults (empty objects, container defaults, SA mounts)
	stripServerDefaults(result)

	return json.Marshal(result)
}

// stripServerDefaults recursively walks a JSON object and removes
// K8S server-defaulted fields that the user didn't specify.
func stripServerDefaults(v interface{}) {
	switch val := v.(type) {
	case map[string]interface{}:
		// Strip imagePullPolicy from containers (objects with both "name" and "image")
		if _, hasName := val["name"]; hasName {
			if _, hasImage := val["image"]; hasImage {
				delete(val, "imagePullPolicy")
			}
		}

		// Strip auto-injected service account volumeMounts
		stripServiceAccountVolumeMounts(val)

		// Recurse into children, then remove keys with empty object values
		for k, child := range val {
			stripServerDefaults(child)
			if isEmptyObject(child) {
				delete(val, k)
			}
		}

	case []interface{}:
		for _, child := range val {
			stripServerDefaults(child)
		}
	}
}

// stripServiceAccountVolumeMounts removes auto-injected service account
// token volumeMounts from a container-like object.
func stripServiceAccountVolumeMounts(obj map[string]interface{}) {
	mounts, ok := obj["volumeMounts"].([]interface{})
	if !ok {
		return
	}

	filtered := make([]interface{}, 0, len(mounts))
	for _, m := range mounts {
		mount, ok := m.(map[string]interface{})
		if !ok {
			filtered = append(filtered, m)
			continue
		}
		mountPath, _ := mount["mountPath"].(string)
		if strings.HasPrefix(mountPath, serviceAccountMountPrefix) {
			continue
		}
		filtered = append(filtered, m)
	}

	if len(filtered) == 0 {
		delete(obj, "volumeMounts")
	} else {
		obj["volumeMounts"] = filtered
	}
}

// stripServerManagedLabels removes labels with server-managed prefixes
// from a metadata object.
func stripServerManagedLabels(meta map[string]interface{}) {
	labels, ok := meta["labels"].(map[string]interface{})
	if !ok {
		return
	}

	for key := range labels {
		for _, prefix := range serverManagedLabelPrefixes {
			if strings.HasPrefix(key, prefix) {
				delete(labels, key)
				break
			}
		}
	}

	if len(labels) == 0 {
		delete(meta, "labels")
	}
}

// stripServerManagedAnnotations removes annotations with server-managed
// prefixes from a metadata object.
func stripServerManagedAnnotations(meta map[string]interface{}) {
	annotations, ok := meta["annotations"].(map[string]interface{})
	if !ok {
		return
	}

	for key := range annotations {
		for _, prefix := range serverManagedAnnotationPrefixes {
			if strings.HasPrefix(key, prefix) {
				delete(annotations, key)
				break
			}
		}
	}

	if len(annotations) == 0 {
		delete(meta, "annotations")
	}
}

// isEmptyObject returns true if v is a map with no entries.
func isEmptyObject(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	return ok && len(m) == 0
}
