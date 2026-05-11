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
	"deletionGracePeriodSeconds",
}

// serverManagedAnnotationPrefixes are annotation key prefixes set by K8S
// controllers asynchronously after resource creation. These must be stripped
// because they may not be present in the Apply result but appear on later
// Get calls, causing false property drift.
var serverManagedAnnotationPrefixes = []string{
	"deployment.kubernetes.io/",
	"deprecated.daemonset.template.generation",
}

// serverManagedLabelPrefixes are label key prefixes set by K8S admission
// controllers. These must be stripped because they cause Formae dot-expansion
// issues (dotted keys get expanded into nested objects) and are not user-managed.
var serverManagedLabelPrefixes = []string{
	"kubernetes.io/",
	"batch.kubernetes.io/",
}

// serverManagedLabelKeys are exact label keys set by K8S controllers
// that should be stripped from metadata and template metadata.
var serverManagedLabelKeys = []string{
	"controller-uid",
	"job-name",
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
		// Strip metadata.finalizers — set by K8S controllers (e.g. kubernetes.io/pvc-protection),
		// not user-managed.
		delete(meta, "finalizers")
		stripServerManagedAnnotations(meta)
		stripServerManagedLabels(meta)
	}

	// Strip server-managed spec.finalizers ONLY for Namespace (its `kubernetes`
	// finalizer is owned by the server, not user-managed). For every other
	// kind `spec.finalizers` is meaningful when a user declares it, so leave
	// it alone.
	//
	// For Namespace specifically: the K8s API server always returns spec with
	// a `finalizers` array; after stripping, spec is left empty. The Namespace
	// PKL schema (k8s/core/Namespace.pkl) does not let users declare spec
	// fields (NamespaceSpec is an empty open class), so removing the empty
	// spec key entirely lets Formae's property comparator match against the
	// user's declaration which has no spec field. This is safe ONLY for
	// Namespace because no other kind ships with an empty user-facing spec.
	if kind == "Namespace" {
		if spec, ok := result["spec"].(map[string]interface{}); ok {
			delete(spec, "finalizers")
			if len(spec) == 0 {
				delete(result, "spec")
			}
		}
	}

	// Strip controller-injected fields from spec based on resource kind.
	stripControllerInjectedFields(result, kind)

	// Recursively strip server defaults (empty objects, container defaults, SA mounts)
	stripServerDefaults(result, "")

	return json.Marshal(result)
}

// containerListPaths are the JSON paths (joined by ".") under which container
// objects can legitimately appear. Stripping imagePullPolicy/SA-mounts only
// happens when traversal reaches one of these — previous heuristic detection
// (any object with "name"+"image" keys) would clobber user maps that happen
// to share those keys.
//
// Path segments use "[]" for array elements. Comparison ignores the literal
// "[]" so e.g. "spec.containers[]" matches every element of the array.
var containerListPaths = map[string]bool{
	"spec.containers":                                         true,
	"spec.initContainers":                                     true,
	"spec.ephemeralContainers":                                true,
	"spec.template.spec.containers":                           true,
	"spec.template.spec.initContainers":                       true,
	"spec.template.spec.ephemeralContainers":                  true,
	"spec.jobTemplate.spec.template.spec.containers":          true,
	"spec.jobTemplate.spec.template.spec.initContainers":      true,
	"spec.jobTemplate.spec.template.spec.ephemeralContainers": true,
}

// podSpecPaths are the JSON paths at which a PodSpec object lives. Used to
// scope the deprecated-serviceAccount stripping rather than duck-typing on
// "has a containers field".
var podSpecPaths = map[string]bool{
	"spec":                              true,
	"spec.template.spec":                true,
	"spec.jobTemplate.spec.template.spec": true,
}

// stripServerDefaults walks a JSON object and removes K8S server-defaulted
// fields that the user didn't specify. The path argument tracks the JSON
// pointer of the current node so we can apply container/PodSpec-specific
// rules only where they're correct (e.g. spec.containers[]) instead of
// heuristically matching any object with a "name"+"image" pair.
func stripServerDefaults(v interface{}, path string) {
	switch val := v.(type) {
	case map[string]interface{}:
		// PodSpec rule: strip deprecated serviceAccount field.
		if podSpecPaths[path] {
			delete(val, "serviceAccount")
		}

		// Container rule: strip imagePullPolicy and auto-injected SA mounts.
		if isContainerElementPath(path) {
			delete(val, "imagePullPolicy")
			stripServiceAccountVolumeMounts(val)
		}

		// Strip server-managed labels/annotations from any metadata-like object
		// (has "labels" or "annotations" as direct children). This handles both
		// top-level metadata and nested metadata (e.g. spec.template.metadata).
		if _, hasLabels := val["labels"]; hasLabels {
			stripServerManagedLabels(val)
		}
		if _, hasAnnotations := val["annotations"]; hasAnnotations {
			stripServerManagedAnnotations(val)
		}

		// Empty-object handling intentionally omitted: apply-config +
		// encoding/json's omitempty normalizes optional maps consistently on
		// both sides, and semantic empties (podSelector:{}, namespaceSelector:{},
		// etc.) survive because their apply-config fields are non-omitempty
		// pointers. Stripping here was redundant and, paired with a restore on
		// Create/Update, caused false sync drift on fields like ConfigMap.binaryData.
		for k, child := range val {
			stripServerDefaults(child, joinPath(path, k))
		}

	case []interface{}:
		// Array elements live "under" the parent path with a "[]" suffix.
		elemPath := path + "[]"
		for _, child := range val {
			stripServerDefaults(child, elemPath)
		}
	}
}

// isContainerElementPath reports whether path identifies a container array
// element. Paths look like "spec.containers[]" or "spec.template.spec.initContainers[]".
func isContainerElementPath(path string) bool {
	if len(path) < 2 || path[len(path)-2:] != "[]" {
		return false
	}
	return containerListPaths[path[:len(path)-2]]
}

// joinPath appends a key onto a dotted JSON path. The root path is "".
func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
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
		stripped := false
		for _, prefix := range serverManagedLabelPrefixes {
			if strings.HasPrefix(key, prefix) {
				delete(labels, key)
				stripped = true
				break
			}
		}
		if stripped {
			continue
		}
		for _, exactKey := range serverManagedLabelKeys {
			if key == exactKey {
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

// stripControllerInjectedFields removes fields injected by K8S controllers
// that are not user-managed. These vary by resource kind.
func stripControllerInjectedFields(result map[string]interface{}, kind string) {
	spec, _ := result["spec"].(map[string]interface{})

	switch kind {
	case "Pod":
		if spec != nil {
			// Deprecated field auto-copied from serviceAccountName
			delete(spec, "serviceAccount")
			// Set by scheduler after pod scheduling
			delete(spec, "nodeName")
			// Injected by admission controllers (e.g. default tolerations)
			delete(spec, "tolerations")
			// Server default — auto-set to 0 by admission controller
			delete(spec, "priority")
			// Server default
			delete(spec, "preemptionPolicy")
			// Server default (true)
			delete(spec, "enableServiceLinks")
			// Server default ("default") — auto-set when not specified
			delete(spec, "serviceAccountName")
			// Auto-injected service account token volume
			stripServiceAccountVolumes(spec)
		}

	case "Job":
		if spec != nil {
			// Auto-generated selector by Job controller
			delete(spec, "selector")
			// Strip controller-injected labels from template metadata
			if tmpl, ok := spec["template"].(map[string]interface{}); ok {
				if tmplMeta, ok := tmpl["metadata"].(map[string]interface{}); ok {
					stripServerManagedLabels(tmplMeta)
				}
			}
		}
		// Strip controller-injected labels from top-level metadata
		if meta, ok := result["metadata"].(map[string]interface{}); ok {
			stripServerManagedLabels(meta)
		}

	case "Service":
		if spec != nil {
			// Assigned by K8S cluster networking, not user-specified
			delete(spec, "clusterIP")
			delete(spec, "clusterIPs")
			// Assigned by K8S based on cluster config
			delete(spec, "ipFamilies")
		}

	case "Secret":
		// K8S converts stringData to base64-encoded data on the server side and
		// never returns stringData. Strip data because it's a server-side transformation
		// of the user-provided stringData — the conformance test expects only what the
		// user specified.
		delete(result, "data")

	case "PersistentVolume":
		if spec != nil {
			// Server default — may be auto-set to empty string
			delete(spec, "storageClassName")
			// Strip hostPath.type server default (empty string)
			if hp, ok := spec["hostPath"].(map[string]interface{}); ok {
				delete(hp, "type")
				if len(hp) == 0 {
					delete(spec, "hostPath")
				}
			}
		}

	case "PersistentVolumeClaim":
		// storageClassName and volumeMode are now handled by hasProviderDefault in the schema
	}
}

// stripServiceAccountVolumes removes auto-injected service account token
// volumes from a PodSpec-like object.
func stripServiceAccountVolumes(spec map[string]interface{}) {
	volumes, ok := spec["volumes"].([]interface{})
	if !ok {
		return
	}

	filtered := make([]interface{}, 0, len(volumes))
	for _, v := range volumes {
		vol, ok := v.(map[string]interface{})
		if !ok {
			filtered = append(filtered, v)
			continue
		}
		// Auto-injected projected volume for SA token has name "kube-api-access-*"
		name, _ := vol["name"].(string)
		if strings.HasPrefix(name, "kube-api-access-") {
			continue
		}
		filtered = append(filtered, v)
	}

	if len(filtered) == 0 {
		delete(spec, "volumes")
	} else {
		spec["volumes"] = filtered
	}
}
