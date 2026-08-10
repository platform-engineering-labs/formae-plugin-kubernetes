# Helm

Two different ways to run a Helm chart from formae. They are not variations on a
theme — they put ownership in different places, and that decides which one you
want.

| | `K8S::Helm::Release` | `HelmChart.pkl` |
|---|---|---|
| Who applies the objects | Helm, via the embedded SDK | formae, via Server-Side Apply |
| What formae manages | one resource: the release | every rendered object, individually |
| Hooks | work — Helm runs them | **ignored**, and leak as permanent resources |
| CRD ordering, subcharts | Helm's problem | reimplemented, incompletely |
| `helm list` / `history` / `rollback` | work against it | see nothing |
| Per-object drift detection | no | yes |
| Typed references into chart output | no | yes |
| Needs `pkl-reader-helm` on PATH | no | yes |

Prefer **`Release`** for charts with hooks, CRDs or subcharts — which is most
real-world charts. Prefer **`HelmChart`** when per-object formae state matters
more than chart fidelity, and you know the chart has no hooks.

---

## `K8S::Helm::Release` — Helm drives it

Formae manages the release; Helm manages the objects the chart renders. The
release is a genuine Helm release (`owner=helm`, `type=helm.sh/release.v1`), so
the Helm CLI works against it normally.

Objects a release renders are hidden from discovery — the release stands in for
them — and listed on its `resourceNames`.

### Single-file examples

| File | Chart source | Shows |
|---|---|---|
| `release-nginx.pkl` | `oci://` reference | the minimal shape; OCI needs no `repoURL` |
| `release-kratos.pkl` | HTTP repo via `repoURL` | weighted `pre-install` hooks, a DB-migration hook, `resource-policy: keep`, and a `test-success` hook that must never be applied |

### Runnable scenarios

Each is a self-contained directory with its own `PklProject`, a script, and a
`make` target that asserts every step against a live cluster.

| Directory | Command | Scenario |
|---|---|---|
| `drift-helm-upgrade/` | `make helm-drift-test` | Deploy version A with formae, `helm upgrade` to B behind its back, watch formae detect the drift and reconcile it back. Shows a release is **not** opaque to drift detection, and that a plain reconcile refuses to clobber an out-of-band change. |
| `immutable-field-upgrade/` | `make helm-immutable-test` | A chart version that changes an immutable field. The upgrade is refused — and so is plain `helm upgrade`, and so is `--force-replace`. Shows the limitation is Kubernetes', not formae's. |
| `adopt-and-rollback/` | `make helm-adopt-test` | `helm install` a release formae knows nothing about, watch the apply get **refused**, then discover → `formae extract` → adopt → upgrade with formae → `helm rollback`. Shows the ownership guard and the whole adoption path. |

Both need `make install` and a running agent. Each has its own README covering
what the scenario proves; read `adopt-and-rollback/README.md` before adopting
anything real — several of its findings are not obvious and cost real time.

### What `Release` does not do

- **No drift detection inside the release.** `kubectl edit` on a chart-owned
  Deployment changes nothing in the release record, so formae reports in sync.
  Helm does not detect it either — hence `helm diff`.
- **No `helm rollback` verb.** Revert the values in your forma and re-apply. Note
  that fires `pre-upgrade` hooks, not `pre-rollback` hooks.
- **No `helm test`.** A CI verb, not desired state.
- **Uninstall leaves residue.** Objects from the chart's `crds/` directory and
  anything annotated `helm.sh/resource-policy: keep` outlive a delete, and
  correctly reappear as unmanaged.
- **Controller-created objects still surface.** The Pods behind a chart's
  Deployment are in no manifest, so discovery lists them individually.
- **A chart that changes an immutable field between versions cannot be
  upgraded.** Not by formae, and not by anything else. Kubernetes freezes some
  fields after creation — `Deployment.spec.selector`, most of
  `PersistentVolumeClaim.spec`, `Service.spec.clusterIP` — and Helm upgrades by
  patching, so the apiserver rejects the change:

  ```
  Deployment.apps "flowise" is invalid: spec.selector: Invalid value:
    v1.LabelSelector{...}: field is immutable
  ```

  This is not formae inheriting a limitation it could route around. Plain
  `helm upgrade` fails identically, and so does Helm's own escape hatch:
  `--force-replace` issues a *replace*, not a delete-and-recreate, and a replace
  cannot change an immutable field either. (In Helm v4 that flag is
  `--force-replace`, renamed from `--force`, and it refuses to run alongside
  server-side apply — which is the default.) The only way through is deleting
  the object and letting the chart recreate it, which no `helm upgrade` will do
  for you.

  It bites on rollback too, and there it is easier to hit by accident: rolling
  back to an older revision means applying an older manifest, and if the field
  changed in the meantime the rollback is refused for the same reason.

  Seen upgrading `flowise` 5.1.1 -> 6.0.0, which renames its selector labels,
  and rolling `helm-dashboard` back over a PersistentVolumeClaim.

  Whether the `HelmChart.pkl` path escapes this is **untested**. In principle
  formae owns each rendered object there and could mark the field `createOnly`
  and replace that one object — but that depends on formae issuing a genuine
  destroy-then-create rather than a replace, and nobody has checked.

---

## `HelmChart.pkl` — formae drives it

Renders the chart with `helm template` and decomposes the output into typed K8s
resources you can `formae apply` like any other workload. Useful for keeping a
chart as the source of truth upstream while managing the rendered output as plain
formae resources.

| File | Chart | K8s version pin |
|---|---|---|
| `nginx.pkl` | bitnami/nginx 22.4.7 | (version-agnostic) |
| `nginx-v1.31.pkl` | bitnami/nginx, fixtures for K8s v1.31 | 1.31 |
| `nginx-v1.34.pkl` | bitnami/nginx, fixtures for K8s v1.34 | 1.34 |
| `postgresql-v1.31.pkl` | bitnami/postgresql | 1.31 |
| `memcached-v1.31.pkl` | bitnami/memcached | 1.31 |

The `-v<minor>` variants exist because charts can emit resources whose shape
depends on the API server's minor version (`policy/v1` vs `policy/v1beta1`). Pick
the variant matching your cluster.

**Hooks are the known limitation.** `helm template` emits hook-annotated
manifests with their annotations intact but performs no orchestration, so this
path applies them as ordinary resources: a `pre-install` Job becomes permanent
and never re-runs, `hook-weight` is ignored, finished hooks accumulate, and
`test` hooks are applied on every reconcile. Charts relying on hooks apply
silently wrong.

### Prerequisites

- `pkl-reader-helm` on `PATH` (see [`helm/README.md`](../../helm/README.md))
- A Helm repo configured:
  ```bash
  helm repo add bitnami https://charts.bitnami.com/bitnami
  helm repo update
  ```

---

## Running the single-file examples

```bash
pkl project resolve examples/helm/
pkl eval --project-dir examples/ examples/helm/release-kratos.pkl
```

**They evaluate, but `formae apply` on them currently fails.**
`examples/PklProject` declares nested local dependencies (`clusters`, `apps`,
`formations`) and evaluating any forma through `formae apply` against it trips a
Pkl bug:

```
org.pkl.core.packages.Dependency$LocalDependency cannot be cast to
  ...$RemoteDependency
```

Every forma under `examples/` is affected, including the pre-existing ones — it is
not specific to the Helm examples. A single local dependency on the plugin's own
schema evaluates fine, which is why `drift-helm-upgrade/` and
`adopt-and-rollback/` carry their own minimal `PklProject` and *are* directly
appliable. Copy that pattern to apply one of the single-file examples.
