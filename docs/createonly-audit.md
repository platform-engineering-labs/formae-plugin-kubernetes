# createOnly mutability audit

Every `@k8s.FieldHint { createOnly = true }` forces formae core to plan a destructive
delete-then-create *replace* when the field changes. A field must be createOnly **only** if
the Kubernetes apiserver genuinely rejects an in-place update to it — otherwise formae
needlessly destroys the resource (data loss / downtime) for a change K8s would have accepted.

Verdicts below were verified empirically on a live cluster: apply the resource, then
`kubectl patch` the field with `--field-manager=formae` and observe accept (mutable → remove
createOnly) vs `field is immutable` / `Forbidden` (immutable → keep).

## Changes applied (over-marked → createOnly removed)

| Kind | Field | apiserver behavior | Action |
|---|---|---|---|
| CSIDriver | `spec.requiresRepublish` | patched (mutable since 1.22) | removed |
| CSIDriver | `spec.tokenRequests` | patched (mutable since 1.22) | removed |
| PriorityClass | `globalDefault` | patched | removed |
| RuntimeClass | `overhead` | patched | removed |
| RuntimeClass | `scheduling` | patched | removed |

## Verified immutable (createOnly kept)

| Kind | Field(s) | apiserver behavior |
|---|---|---|
| CSIDriver | `attachRequired` (+ `podInfoOnMount`, `fsGroupPolicy`, `storageCapacity`, `volumeLifecycleModes`, `seLinuxMount`) | `field is immutable` |
| PriorityClass | `value`, `preemptionPolicy` | `Forbidden` / `field is immutable` |
| RuntimeClass | `handler` | `field is immutable` |
| IngressClass | `spec.controller` | `field is immutable` |
| StorageClass | `provisioner`, `parameters`, `reclaimPolicy`, `volumeBindingMode` | `field is immutable` |
| Service | `clusterIP`, `clusterIPs`, `ipFamilies`, `ipFamilyPolicy`, `loadBalancerClass`, `healthCheckNodePort` | immutable once assigned |
| Deployment / StatefulSet / ReplicaSet | `spec.selector` (+ StatefulSet `serviceName`, `volumeClaimTemplates`, `podManagementPolicy`) | `field is immutable` |
| PersistentVolumeClaim | `accessModes`, `storageClassName`, `volumeMode`, `volumeName`, `selector`, `dataSource`, `dataSourceRef` (NOT `resources` — storage expansion stays in-place) | PVC spec immutable except storage expansion |
| Job | `template`, `completions`, `completionMode`, `selector`, `podFailurePolicy`, `backoffLimitPerIndex`, `managedBy` | Job spec immutable post-create |
| RBAC | `RoleBinding.roleRef`, `ClusterRoleBinding.roleRef` | immutable |
| all | `metadata.name`, Namespace `metadata.name`, Secret `type` / immutable `data` | identity / immutable |

## Notes

- Several fields that ARE mutable in K8s (StorageClass `allowVolumeExpansion`/`mountOptions`,
  Service `internalTrafficPolicy`, IngressClass `spec.parameters`) were already **not** marked
  createOnly — no change needed.
- Data-bearing replaces that remain (PVC storageClass/accessModes change, StatefulSet
  `volumeClaimTemplates` change) are genuinely immutable in K8s; policy is "graceful where K8s
  allows". StatefulSet delete retains `volumeClaimTemplates` PVCs (Foreground propagation), so
  an STS replace preserves data — covered by `statefulset_replace_integration_test.go`.
