# Helm

A chart runs as one resource — `K8S::Helm::Release` — installed and upgraded
through the Helm SDK embedded in the plugin:

```pkl
import "@k8s/helm/Release.pkl" as helm
```

The version segment matches your cluster minor, like every other schema import.
No reader binary is needed — nothing is rendered at Pkl-eval time.

> The earlier `HelmChart.pkl` path, which rendered a chart client-side and
> decomposed it into individually-managed typed resources, has been removed. It
> could not honour hooks: `helm template` emits hook-annotated manifests with no
> orchestration, so a `pre-install` Job became a permanent resource, hook weights
> were ignored, and `test` hooks were applied on every reconcile. Charts that
> relied on hooks applied silently wrong.

## How it works

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

All need `make install`. `helm-drift-test` and `helm-adopt-test` need an agent
already running; `helm-immutable-test` starts its own. Each has its own README covering
what the scenario proves; read `adopt-and-rollback/README.md` before adopting
anything real — several of its findings are not obvious and cost real time.

### What `Release` does not do

- **No drift detection inside the release.** `kubectl edit` on a chart-owned
  Deployment changes nothing in the release record, so formae reports in sync.
  Helm does not detect it either — hence `helm diff`.
- **No `helm rollback` verb.** Revert the values in your forma and re-apply. Note
  that fires `pre-upgrade` hooks, not `pre-rollback` hooks.

  **Checked** in `test/helm-interop/`, via the verb Helm records for each
  revision — the verb being what decides the hook event. Every chart asserts that
  formae's own upgrade was recorded as an upgrade and never a rollback, and
  velero additionally asserts that the out-of-band `helm rollback` *was* recorded
  as a rollback, which is the only revision in the scenario that fires
  `pre-rollback` hooks at all.

  What that leaves: the release formae moves is moved *forwards*. A
  formae-performed reverse move is not exercised, because the reconcile that
  would do it is refused by the drift guard in every run observed so far
  (22 of 22 charts in CI). The mechanism is the same either way — one Helm
  upgrade action, no rollback verb — but the reverse direction is inference, not
  measurement.

  Also still not covered: a `post-rollback` hook. No chart in the set carries one
  and the corpus cannot supply one — see `test/helm-interop/charts/README.md`.
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

### What happens if something dies mid-install

Helm has no server-side operation controller. The install runs inside the plugin
process, so if that process dies the work dies with it, and Helm's
`pending-install` status is left behind as a lock with no owner — Helm then
refuses both install *and* upgrade on that release.

The plugin clears that lock itself on the next operation. What it settles on is
decided by the cluster, because Helm writes `deployed` last:

```
Releases.Create(pending-install) -> hooks -> create objects -> SetStatus(deployed)
```

Dying anywhere in the middle leaves an identical record whether the work
finished or never started, so recovery looks at the objects rather than the
record:

| Objects the release renders | What happens |
|---|---|
| All present and ready | The install *did* finish and only the record was lost. Recorded `deployed`, reported as success — no second Helm operation, no hooks re-run |
| Anything missing | Recorded `failed`, which an upgrade runs over, three-way merging what is absent |

**What you can rely on**, whatever died:

1. The command reaches a verdict — it never sits in progress on work nothing is
   doing.
2. A failure says why, naming the missing object.
3. The next apply converges. No `helm uninstall`, no manual step.

**What to know before you rely on it:**

- **A plugin crash defers recovery to the next apply.** The agent's operator runs
  on the plugin's node and dies with it, so the command fails without the plugin
  being asked again. The release stays `pending-install` until something applies
  — which then recovers it automatically. That failure currently carries no
  message, because it comes from the agent rather than the plugin.
- **`formae agent stop` mid-install is a crash**, not a graceful stop: it reaches
  the plugin as `SIGKILL` via the supervisor, so there is no opportunity to
  unwind. A `SIGTERM` sent directly to the plugin — a container runtime stopping
  the pod, systemd — *is* handled, and lands the release on `failed` instead.
- **A release formae did not install is never rewritten.** Someone else's stuck
  operation is reported with the recovery command, and left alone.
- **`atomic = true`** makes a cancelled install try to uninstall itself on the
  way out, bounded by `timeoutSeconds`, which can outlast the shutdown it is
  racing.

This is covered end to end by `make helm-stability-test`, which kills real agent
and plugin processes and asserts the release is never left holding a lock.

---

## Running the single-file examples

```bash
pkl project resolve examples/helm/
pkl eval --project-dir examples/ examples/helm/release-kratos.pkl
pkl eval --project-dir examples/ examples/helm/release-nginx.pkl
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
