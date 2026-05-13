// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ObservedGenerationReady reports whether the controller has observed and
// reconciled the latest spec generation. It returns true when the controller's
// reported observedGeneration is at or beyond the object's metadata.generation,
// meaning any status fields can be trusted as reflecting the current spec.
//
// Use this as the first gate in operationStatus helpers for controller-driven
// resources (Deployment, StatefulSet, DaemonSet, etc.) before inspecting
// replica counts or conditions. When this returns false, the resource is
// effectively InProgress regardless of what the (stale) status reports.
func ObservedGenerationReady(meta metav1.Object, observedGeneration int64) bool {
	return observedGeneration >= meta.GetGeneration()
}
