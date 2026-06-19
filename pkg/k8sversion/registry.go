// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Package k8sversion mirrors the @K8sVersion annotations declared in the
// PKL schemas at schema/pkl/. The Go-side registry is the runtime authority
// for field-gate preflight checks: when a user submits a Pod payload that
// includes a field gated to K8s 1.28+, and the cluster reports 1.27, the
// plugin returns a clear client-side error instead of an opaque API server
// rejection.
//
// The registry is hand-maintained and kept in sync with PKL via a future
// CI drift detector (RFC docs/rfc-k8s-version-support.md, Section 4).
package k8sversion

import (
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
)

// Gate is the runtime equivalent of the PKL `@K8sVersion` annotation.
// Empty strings mean "no gate on that side."
type Gate struct {
	IntroducedIn string
	DeprecatedIn string
	RemovedIn    string
	Reference    string
}

// gateKey identifies a gated field by resource type ("K8S::Core::Pod") and
// dotted JSON path within properties ("spec.resourceClaims").
type gateKey struct {
	ResourceType string
	FieldPath    string
}

// registry maps (resourceType, fieldPath) → Gate.
//
// IMPORTANT: Every entry here MUST mirror an `@K8sVersion` annotation in the
// PKL schema at schema/pkl/<group>/<Resource>.pkl (or in the shared sub-types
// in schema/pkl/k8s.pkl). When adding or modifying a PKL annotation, update
// this map in the same commit.
//
// This hand-maintained mirror is a temporary bridge; once the Formae core
// `Schema Hint Metadata` RFC ships, this registry is replaced by a runtime
// read from `model.Schema.Hints[path].Metadata["k8s.io/version"]`.
// See docs/rfc-k8s-version-implementation.md.
var registry = map[gateKey]Gate{
	// Pod fields (defined in schema/pkl/k8s.pkl, PodSpec class)
	{ResourceType: "K8S::Core::Pod", FieldPath: "spec.resourceClaims"}: {
		IntroducedIn: "1.26",
		Reference:    "https://kep.k8s.io/3063",
	},
	{ResourceType: "K8S::Core::Pod", FieldPath: "spec.schedulingGates"}: {
		IntroducedIn: "1.26",
		Reference:    "https://kep.k8s.io/3521",
	},
	{ResourceType: "K8S::Core::Pod", FieldPath: "spec.hostUsers"}: {
		IntroducedIn: "1.25",
		Reference:    "https://kep.k8s.io/127",
	},
	{ResourceType: "K8S::Core::Pod", FieldPath: "spec.securityContext.appArmorProfile"}: {
		IntroducedIn: "1.30",
		Reference:    "https://kep.k8s.io/24",
	},
	{ResourceType: "K8S::Core::Pod", FieldPath: "spec.containers.#.resizePolicy"}: {
		IntroducedIn: "1.27",
		Reference:    "https://kep.k8s.io/1287",
	},
	{ResourceType: "K8S::Core::Pod", FieldPath: "spec.initContainers.#.resizePolicy"}: {
		IntroducedIn: "1.27",
		Reference:    "https://kep.k8s.io/1287",
	},

	// Job fields (defined in schema/pkl/k8s.pkl, JobSpec class)
	{ResourceType: "K8S::Batch::Job", FieldPath: "spec.managedBy"}: {
		IntroducedIn: "1.30",
		Reference:    "https://kep.k8s.io/4368",
	},
	{ResourceType: "K8S::Batch::Job", FieldPath: "spec.maxFailedIndexes"}: {
		IntroducedIn: "1.28",
		Reference:    "https://kep.k8s.io/3850",
	},
	{ResourceType: "K8S::Batch::Job", FieldPath: "spec.successPolicy"}: {
		IntroducedIn: "1.30",
		Reference:    "https://kep.k8s.io/3998",
	},

	// Service fields (defined in schema/pkl/core/Service.pkl)
	{ResourceType: "K8S::Core::Service", FieldPath: "spec.externalIPs"}: {
		DeprecatedIn: "1.36",
		Reference:    "https://github.com/kubernetes/enhancements/issues/1864",
	},
	{ResourceType: "K8S::Core::Service", FieldPath: "spec.trafficDistribution"}: {
		IntroducedIn: "1.30",
		Reference:    "https://kep.k8s.io/4444",
	},
}

// typeRegistry maps a resource type to its type-level @K8sVersion gate — the
// module-scoped annotation on the resource's PKL schema (introducedIn /
// removedIn for the whole kind, not a single field).
//
// IMPORTANT: like `registry` above, this is a hand-maintained mirror of the
// PKL schema. Every entry MUST match a module-level `@K8sVersion` annotation
// in schema/pkl-main/<group>/<Resource>.pkl; update this map in the same
// commit that adds or changes one.
var typeRegistry = map[string]Gate{
	"K8S::Admissionregistration::MutatingAdmissionPolicy": {
		IntroducedIn: "1.36",
		Reference:    "https://kep.k8s.io/3962",
	},
	"K8S::Flowcontrol::FlowSchema": {
		IntroducedIn: "1.29",
		Reference:    "https://kubernetes.io/blog/2024/04/30/kubernetes-1-30-release-note-flow-control-graduations/",
	},
	"K8S::Flowcontrol::PriorityLevelConfiguration": {
		IntroducedIn: "1.29",
		Reference:    "https://kubernetes.io/blog/2024/04/30/kubernetes-1-30-release-note-flow-control-graduations/",
	},
	"K8S::Autoscaling::HorizontalPodAutoscaler": {
		IntroducedIn: "1.23",
		Reference:    "https://github.com/kubernetes/enhancements/issues/117",
	},
}

// LookupType returns the type-level Gate for a resource type, or
// (Gate{}, false) if the type is not version-gated.
func LookupType(resourceType string) (Gate, bool) {
	g, ok := typeRegistry[resourceType]
	return g, ok
}

// TypeSupported reports whether a resource type is available on a cluster
// running clusterVersion. Ungated types are always supported. When a gate
// applies and the cluster version is out of range, it returns false plus a
// human-readable reason for use in mutating-operation errors.
//
// clusterVersion must be normalized via config.NormalizeK8sVersion.
func TypeSupported(resourceType, clusterVersion string) (bool, string) {
	gate, ok := LookupType(resourceType)
	if !ok {
		return true, ""
	}
	ref := ""
	if gate.Reference != "" {
		ref = " (see " + gate.Reference + ")"
	}
	if gate.IntroducedIn != "" && config.CompareK8sVersions(clusterVersion, gate.IntroducedIn) < 0 {
		return false, fmt.Sprintf("%s requires Kubernetes %s, target reports %s%s",
			resourceType, gate.IntroducedIn, clusterVersion, ref)
	}
	if gate.RemovedIn != "" && config.CompareK8sVersions(clusterVersion, gate.RemovedIn) >= 0 {
		return false, fmt.Sprintf("%s was removed in Kubernetes %s, target reports %s%s",
			resourceType, gate.RemovedIn, clusterVersion, ref)
	}
	return true, ""
}

// PathsForResource returns every gated field path registered for a resource
// type. Used by the generic preflight walker so individual provisioners do
// not need to enumerate gated fields by hand.
func PathsForResource(resourceType string) []string {
	var paths []string
	for k := range registry {
		if k.ResourceType == resourceType {
			paths = append(paths, k.FieldPath)
		}
	}
	return paths
}

// Lookup returns the Gate for a given resource type + field path.
// Returns (Gate{}, false) if the field is not gated.
func Lookup(resourceType, fieldPath string) (Gate, bool) {
	g, ok := registry[gateKey{ResourceType: resourceType, FieldPath: fieldPath}]
	return g, ok
}

// CheckField evaluates a single field against the cluster K8s version.
// Returns nil when the field is allowed, or an error describing why the
// cluster version is incompatible with the field's gate.
//
// clusterVersion must be normalized via config.NormalizeK8sVersion before
// calling.
func CheckField(resourceType, fieldPath, clusterVersion string) error {
	gate, ok := Lookup(resourceType, fieldPath)
	if !ok {
		return nil
	}
	if gate.IntroducedIn != "" && config.CompareK8sVersions(clusterVersion, gate.IntroducedIn) < 0 {
		ref := ""
		if gate.Reference != "" {
			ref = " (see " + gate.Reference + ")"
		}
		return fmt.Errorf("field %q on %s requires Kubernetes %s, cluster reports %s%s",
			fieldPath, resourceType, gate.IntroducedIn, clusterVersion, ref)
	}
	if gate.RemovedIn != "" && config.CompareK8sVersions(clusterVersion, gate.RemovedIn) >= 0 {
		ref := ""
		if gate.Reference != "" {
			ref = " (see " + gate.Reference + ")"
		}
		return fmt.Errorf("field %q on %s was removed in Kubernetes %s, cluster reports %s%s",
			fieldPath, resourceType, gate.RemovedIn, clusterVersion, ref)
	}
	return nil
}

// CheckPaths runs CheckField for every supplied field path. Returns the first
// error encountered, or nil if all paths pass. Callers that want all errors
// at once can call CheckField directly per path.
func CheckPaths(resourceType string, fieldPaths []string, clusterVersion string) error {
	for _, fp := range fieldPaths {
		if err := CheckField(resourceType, fp, clusterVersion); err != nil {
			return err
		}
	}
	return nil
}
