# Per-K8s-version Feature Fixtures

Each subdirectory exercises **one** version-gated Kubernetes API field. The
testdata generator (`tools/gen-versioned-testdata/`) reads each feature's
`meta.json` and emits the directory under `testdata/generated/v<X.Y>/features/`
only when the target K8s minor satisfies the meta.

## Feature index

| Directory | Field | minK8sVersion | KEP |
|---|---|---|---|
| `cronjob-time-zone` | `CronJob.spec.timeZone` | 1.27 | [3140](https://kep.k8s.io/3140) |
| `job-managed-by` | `Job.spec.managedBy` | 1.32 | [4368](https://kep.k8s.io/4368) |
| `job-max-failed-indexes` | `Job.spec.maxFailedIndexes` | 1.33 | [3850](https://kep.k8s.io/3850) |
| `job-success-policy` | `Job.spec.successPolicy` | 1.33 | [3998](https://kep.k8s.io/3998) |
| `pdb-unhealthy-eviction` | `PodDisruptionBudget.spec.unhealthyPodEvictionPolicy` | 1.31 | [3017](https://kep.k8s.io/3017) |
| `pod-apparmor-profile` | `Pod.spec.securityContext.appArmorProfile` | 1.31 | [24](https://kep.k8s.io/24) |
| `pod-host-users` | `Pod.spec.hostUsers` | 1.33 | [127](https://kep.k8s.io/127) |
| `pod-resize-policy` | `Container.resizePolicy` | 1.33 | [1287](https://kep.k8s.io/1287) |
| `pod-resource-claims` | `Pod.spec.resourceClaims` | 1.34 | [3063](https://kep.k8s.io/3063) |
| `pod-scheduling-gates` | `Pod.spec.schedulingGates` | 1.30 | [3521](https://kep.k8s.io/3521) |
| `statefulset-ordinals` | `StatefulSet.spec.ordinals` | 1.32 | [3335](https://kep.k8s.io/3335) |
| `statefulset-pvc-retention` | `StatefulSet.spec.persistentVolumeClaimRetentionPolicy` | 1.32 | [1847](https://kep.k8s.io/1847) |
| `volume-mount-recursive-readonly` | `VolumeMount.recursiveReadOnly` | 1.34 | [3857](https://kep.k8s.io/3857) |
| `webhook-match-conditions` | `ValidatingWebhookConfiguration.webhooks.matchConditions` | 1.30 | [3716](https://kep.k8s.io/3716) |

`minK8sVersion` is the first K8s minor where the field is **GA** (or default-on
Beta when no GA exists yet). Field is annotated in `schema/pkl/main/` with the
alpha-introduction value (per apple/pkl-k8s convention) so it surfaces in the
schema starting alpha; conformance is gated to GA so the KinD cluster doesn't
need an alpha/beta feature gate enabled.

## Adding a new feature

1. Create `testdata/main/features/<feature-name>/`.
2. Write `meta.json`:
   ```json
   {
     "minK8sVersion": "1.32",
     "description": "..."
   }
   ```
3. Add one or more fixtures (`.pkl` files). Standard imports:
   ```pkl
   amends "@formae/forma.pkl"
   import "@formae/formae.pkl"
   import "@k8s/<group>/<Kind>.pkl" as <alias>
   import "@k8s/k8s.pkl" as k8s
   import "../../shared/config/vars.pkl" as v
   ```
   The config import points to `shared/config/vars.pkl` from a feature dir
   two levels deep.
4. (Optional) Add `<name>-update.pkl` to exercise the Update lifecycle for
   mutable fields. The conformance harness picks it up automatically.
5. Regenerate:
   ```bash
   make generate-versioned-schemas
   ```
6. Verify locally:
   ```bash
   make verify-fixtures        # pkl-evals every feature in every version
   make verify-generated-schemas
   ```
7. Commit `testdata/main/features/<name>/` AND `testdata/generated/`.

## meta.json schema

| Field | Type | Required | Notes |
|---|---|---|---|
| `minK8sVersion` | `String` (MAJOR.MINOR) | one of min/max | Inclusive. e.g. `"1.30"` includes 1.30+. |
| `maxK8sVersion` | `String` (MAJOR.MINOR) | one of min/max | Exclusive. e.g. `"1.36"` includes only versions strictly less than 1.36. |
| `description` | `String` | recommended | Free text, surfaces in code review. |

## Feature-fixture conventions

- One gated field per feature directory.
- One Pod / Job / Service / etc. per fixture file.
- Use `registry.k8s.io/pause:3.10` for placeholder containers — minimal pull.
- Resource names must include `\(v.testRunID)` to avoid cross-test collisions.
- For Update fixtures, the fixture must produce the **same NativeID**
  (metadata.name + metadata.namespace) as the Create fixture so the
  conformance harness performs an in-place update rather than a delete+create.
- `failurePolicy = "Ignore"` and `sideEffects = "None"` for any
  ValidatingWebhookConfiguration / MutatingWebhookConfiguration fixture so
  test-cluster admission can't break itself.

## Running a single feature locally

```bash
FORMAE_TEST_TESTDATA_DIR=testdata/generated/v1.34/features/pod-resource-claims \
FORMAE_K8S_VERSION=1.34 \
  make conformance-test-crud-run
```
