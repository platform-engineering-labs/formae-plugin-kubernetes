# Expand K8S Plugin Discovery Filters

**Date:** 2026-04-19
**Status:** Draft
**Authors:** Jeroen Soeters

## Problem

Formae's discovery loop lists every resource the plugin knows how to list and
surfaces unmanaged ones to the user as candidates for import. For this to be
useful, discovery should only surface resources a user could meaningfully bring
under management. Surfacing noise (controller-created children, system-owned
cluster infrastructure, auto-generated tokens) wastes pipeline capacity and
confuses the user.

The plugin already filters a handful of obvious cases — system namespaces, the
`default` ServiceAccount per namespace, the `kubernetes` API Service,
`system:*` ClusterRoles/ClusterRoleBindings. The list is incomplete.

Concretely, against a stock production cluster discovery currently surfaces:

- Every Pod the Deployment/StatefulSet/DaemonSet/Job controllers created —
  often hundreds, none user-owned.
- Every ReplicaSet a Deployment created (and every old ReplicaSet from past
  rollouts, retained per `revisionHistoryLimit`).
- Every Job a CronJob scheduled (retained per `successfulJobsHistoryLimit` /
  `failedJobsHistoryLimit`).
- The Endpoints object K8S auto-maintains for every Service.
- The `kubernetes.io/service-account-token` Secret K8S generates for token
  projection.
- Leases the control plane uses for leader election (`kube-controller-manager`,
  `kube-scheduler`, etc., all in `kube-system`).
- FlowSchemas and PriorityLevelConfigurations that ship with the apiserver
  (`exempt`, `system-*`, `global-default`).
- PriorityClasses the cluster ships (`system-cluster-critical`,
  `system-node-critical`).

None of these are candidates for formae management. All have universal,
K8S-native signals that identify them.

## Goals

- Filter controller-owned children and system-owned cluster infrastructure from
  discovery.
- Use signals that are universal across K8S distributions — no per-provider or
  per-operator heuristics.
- Keep filter definitions declarative (via `plugin.MatchFilter`) so the agent
  evaluates them at discovery time, consistent with the AWS plugin pattern.

## Non-Goals

- Filtering operator-installed resources (cert-manager, Istio, ArgoCD, etc.).
  Operator-specific filtering belongs in the user's discovery configuration,
  not in the plugin.
- Filtering cloud-provider-installed CSIDrivers, StorageClasses,
  IngressClasses. No universal K8S-native signal exists, filter accuracy would
  vary by cluster type, and legitimate "manage the provider-installed default"
  use cases exist.
- Filtering StatefulSet-created PVCs. K8S omits `ownerReferences` on these by
  design so the data outlives the StatefulSet — PVCs are first-class resources
  users legitimately want to track independently.

## Design

### The signal

K8S has one universal mechanism for expressing "this resource is managed by
another resource": `metadata.ownerReferences`. Every well-behaved controller
sets it. The API server's garbage collector uses it to cascade-delete children
when the parent is deleted. Unlike labels (advisory, inconsistent) or name
conventions (per-controller), ownerReferences are a first-class K8S concept.

All controller-child relationships we care about use it:

| Child                    | Owner(s)                                    | Signal                                    |
| ------------------------ | ------------------------------------------- | ----------------------------------------- |
| ReplicaSet               | Deployment                                  | any `ownerReferences` entry               |
| Pod                      | ReplicaSet, Job, DaemonSet, StatefulSet     | any `ownerReferences` entry               |
| Endpoints                | Service (EndpointSlice controller)          | any `ownerReferences` entry               |
| Job                      | CronJob                                     | `ownerReferences[?kind == "CronJob"]`     |

Standalone Pods and standalone Jobs (one-off migrations, manual debug pods)
have no `ownerReferences` and are discovered as expected.

### The six filters

**1. ReplicaSet with any owner reference.** A Deployment-managed ReplicaSet.

```
ResourceTypes: ["K8S::Apps::ReplicaSet"]
PropertyPath:  "$.metadata.ownerReferences[0]"
```

**2. Pod with any owner reference.** Covers pods created by ReplicaSet, Job,
DaemonSet, StatefulSet — all well-behaved controllers set owner refs.

```
ResourceTypes: ["K8S::Core::Pod"]
PropertyPath:  "$.metadata.ownerReferences[0]"
```

**3. Endpoints with any owner reference.** The EndpointSlice controller
maintains Endpoints for every Service and sets the ownerReference to the
Service.

```
ResourceTypes: ["K8S::Core::Endpoints"]
PropertyPath:  "$.metadata.ownerReferences[0]"
```

**4. Job owned by a CronJob.** Scheduled tick Jobs, not standalone Jobs.

```
ResourceTypes: ["K8S::Batch::Job"]
PropertyPath:  "$.metadata.ownerReferences[?@.kind == \"CronJob\"]"
```

**5. Service account token Secret.** K8S auto-creates these for token
projection; the `Secret.type` field is authoritative.

```
ResourceTypes: ["K8S::Core::Secret"]
PropertyPath:  "$.type"
PropertyValue: "kubernetes.io/service-account-token"
```

**6. Leases in `kube-system`.** The `kube-system` namespace is the control
plane's, and every Lease in it is a leader-election artifact from a system
controller (kube-controller-manager, kube-scheduler, cloud-controller-manager,
node leases, etc.).

```
ResourceTypes: ["K8S::Coordination::Lease"]
PropertyPath:  "$.metadata.namespace"
PropertyValue: "kube-system"
```

### System cluster-scoped resources

Three more cluster-scoped resource types ship with the apiserver and are
conventionally named `system-*`. The filter reuses the existing `search()`
pattern the plugin already uses for `system:*` RBAC:

**7. FlowSchema with `system-*` name.**
**8. PriorityLevelConfiguration with `system-*` name.**

```
ResourceTypes: ["K8S::Flowcontrol::FlowSchema"]       // and PriorityLevelConfiguration
PropertyPath:  "$.metadata[?search(@, '^system-')]"
```

FlowSchemas also include `exempt` and `global-default` — two explicit name
matches cover those.

**9. PriorityClass with `system-*` name.** K8S defines two by default
(`system-cluster-critical`, `system-node-critical`); matching on the
`system-` prefix is safe and future-proof.

```
ResourceTypes: ["K8S::Scheduling::PriorityClass"]
PropertyPath:  "$.metadata[?search(@, '^system-')]"
```

### Pod and ReplicaSet: client-side List filtering too

Discovery filters evaluated by the agent are the declarative safety net. For
high-volume types, filtering at the plugin's `List` boundary prevents formae
from even shipping the names across the wire. The existing ClusterRole
provisioner does this at `pkg/resources/rbac/clusterrole.go:204-210` — it
skips `system:*` names in `List()` before returning native IDs.

Pods and ReplicaSets have the highest volume in a typical cluster (Pods: often
100-1000+; ReplicaSets: roughly 3x Deployments). Adding the same
"`ownerReferences != nil` ⇒ skip" filter in their `List()` methods matches the
existing pattern and keeps discovery fast. Endpoints and Jobs are lower
volume; the declarative filter alone is sufficient.

## Alternatives Considered

**Label-based filtering instead of ownerReferences.** Labels like
`app.kubernetes.io/managed-by` exist but are advisory and inconsistent across
controllers. ownerReferences are set by every well-behaved controller and
drive K8S's own garbage collection. It is the canonical signal.

**Filter cloud-provider-installed StorageClasses and CSIDrivers.** Rejected.
No universal signal — name patterns and provisioner names vary by cloud
platform, operator-installed drivers look identical to
provider-default-installed ones, and legitimate "manage the default `gp3`"
use cases exist. The volume is tiny (a handful per cluster) so there is no
pipeline pressure. Leaving them in discovery lets the user decide.

**Filter StatefulSet-created PVCs.** Rejected.
`persistentVolumeClaimRetentionPolicy` defaults to `Retain` — K8S deliberately
does *not* set ownerReferences on volumeClaimTemplate PVCs by default so data
outlives the StatefulSet. PVCs are designed to have an independent lifecycle;
surfacing them in discovery is correct.

**Filter operator-installed resources (cert-manager, Istio, etc.).** Rejected
for this plugin. Per-operator filtering varies by cluster and by operator
version. If formae wants to offer this, it belongs as user-configurable
discovery rules, not hard-coded in the plugin.

**Filter CustomResourceDefinitions with `*.k8s.io` group suffix.** Rejected.
The intuition was "K8S-shipped CRDs are noise, filter by their API group." On
inspection the intuition does not hold: K8S itself registers most built-in
types (`Pod`, `Service`, `Deployment`, …) as native apiserver types, not as
CRDs, so they never appear in `kubectl get crd`. The CRDs that *do* exist
under `*.k8s.io` groups in a real cluster are almost exclusively
user-installed (Gateway API, historical cert-manager, snapshot/CSI
extensions). Filtering by group suffix would hide things users legitimately
want to manage while catching almost nothing they don't — a bad trade.

**Client-side List filtering only, no declarative filter.** Rejected. The
declarative filter is the serializable, agent-evaluated safety net that works
even if a plugin version regresses in its `List` filter. The existing
`system:*` ClusterRole implementation uses both, for the same reason — we
extend that precedent.

## Testing

- Unit tests asserting each filter's presence, resource types, and condition
  shape, using a structural match against `Plugin.DiscoveryFilters()`.
- Integration tests for the Pod and ReplicaSet client-side `List` filters:
  create owned and unowned instances, assert only unowned appear in
  `List()` native IDs. Follows the existing `rbac_integration_test.go`
  pattern.
- A conformance test covering the end-to-end path: create a Deployment (which
  the ReplicaSet controller expands into a ReplicaSet and Pods), run
  `formae discover`, assert the Deployment appears and its child ReplicaSet
  and Pods do not.
