# Rollout-safety examples

One folder per production-update-safety case. Each is provisioned with the
**formae Kubernetes plugin** (PKL formae), so you can watch the plugin's own
status reporting change between the old and new plugin. `kubectl` is used only
for *external drift* (case 05) and for eyeballing raw K8s state.

| Case | Folder | Scenario | Old plugin | New plugin |
|---|---|---|---|---|
| 1 | `01-paused-deployment` | pause + bump image | `InProgress` forever | `Success` once observed |
| 2 | `02-ondelete-statefulset` | `updateStrategy: OnDelete` + bump image | `InProgress` forever | `Success` once ready |
| 3 | `03-partition-statefulset` | `rollingUpdate.partition: 2` + bump image | `InProgress` forever | `Success` at updated = replicas−partition |
| 4 | `04-wedged-statefulset` | bump to a bad image | `InProgress` forever, no reason | `Failure` with pod reason |
| 5 | `05-hpa-deployment` | HPA scales; forma omits replicas | perpetual drift vs HPA | no drift (replicas not formae-owned) |

Each folder has `create.pkl` (initial state) and, where a change is involved,
`update.pkl`. Every file's header comments the exact before/after. Case 05 adds
`drift.sh` — the external `kubectl` drift.

## Prerequisites

- A cluster on your current kubectl context (OrbStack / kind / minikube). Cases
  override context with `--prop context=<name>`.
- The formae agent running with this plugin installed. To test the **new**
  behavior, build+install this branch first:
  ```bash
  make install          # builds bin/ and installs to ~/.pel/formae/plugins/
  # start the formae agent per your setup, then:
  ```
  For the **old** behavior, install the plugin built from `main`.

## Run a case (example: 01)

```bash
formae apply examples/rollout-safety/01-paused-deployment/create.pkl
# watch it settle, then apply the change:
formae apply examples/rollout-safety/01-paused-deployment/update.pkl
# observe the command status reported by the plugin:
formae status command --query 'client:me'
```

Eyeball the underlying K8s signal the plugin keys off:

```bash
kubectl get deploy paused-demo \
  -o jsonpath='{"gen="}{.metadata.generation}{" observed="}{.status.observedGeneration}{" updated="}{.status.updatedReplicas}{"\n"}'
```

Case 05 external drift:

```bash
formae apply examples/rollout-safety/05-hpa-deployment/create.pkl
bash examples/rollout-safety/05-hpa-deployment/drift.sh
# then re-reconcile the rollout-hpa stack and check for spec.replicas drift
```

## Validation status

- All `create.pkl`/`update.pkl` are schema-validated (`pkl eval`).
- The plugin behavior each case demonstrates is covered by integration tests
  that hit the same provisioner code against a live cluster:
  `pkg/resources/apps/rollout_failure_integration_test.go` (case 4),
  `hpa_coexist_integration_test.go` (case 5), and the `*_status_test.go`
  unit tests (cases 1–3). See `docs/createonly-audit.md` for the Part-A audit.
- End-to-end `formae apply` was not run here (no agent in this environment); run
  it in yours with the agent up.
