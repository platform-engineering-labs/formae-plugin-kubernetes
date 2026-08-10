# Interop chart specs

One file per chart. `<name>.yaml` is the release as `helm install` should first
create it; a `<name>-migrate.yaml` sibling names the version to move to, and its
presence is what opts the chart into the rest of the chain — formae upgrade,
helm rollback, reconcile. Adding a chart is adding a file.

Charts install bare unless the spec carries a `values:` block. A chart that will
not stand up bare is a prerequisite problem, not a formae one, so the cell skips
rather than failing — and its spec file is where to fix it, by adding the values
that make it installable.

## Choosing charts

`trait` says why the chart is in the set, and `traitObject` names the object the
assertion looks at. A cell whose assertions never touch its trait is padding: two
charts running identical assertions differ only in how long they take.

Trait scarcity is worth knowing before adding more. Across a 1000-chart Artifact
Hub sample, exactly two charts declare `pre-rollback` hooks (velero, kubedb) and
one declares `crd-install` (ambassador). The traits that matter most to formae's
rollback and CRD story are the least represented in the wild.

## What the traits actually assert

Selecting a chart for a trait is not the same as testing that trait, and right
now only one of them is checked in a way that can fail.

| trait | charts | assertion |
|---|---|---|
| `test-hook` | 18 | **real** — fails if the hook object was ever applied |
| `pre-rollback` | 1 (velero) | logs whether the object is present; cannot fail |
| `crd-install` | 1 (cert-manager) | logs; cannot fail |
| `no-hooks` | 5 | nothing to assert — the control |
| `post-rollback` | **0** | no chart at all |

Every chart still runs the whole chain — install, discover, adopt, upgrade,
rollback, drift, converge — and those steps are asserted properly. What the table
above is about is the extra, trait-specific assertion at the end.

### post-rollback has no chart, and the corpus cannot supply one

`stackgres-operator` was the last candidate in a thousand charts and it declares
`kubeVersion: 1.18.0-0 - 1.35.x` while CI runs Kubernetes 1.36 — it cannot
install on the version that gates the branch. The others were removed for their
own reasons (see below).

Filling this needs a **local fixture chart** rather than a corpus one: two
versions, a post-rollback hook Job, nothing else. `testdata/charts/hooked`,
which the Go integration tests use, is the pattern. It would run in seconds
instead of minutes and could carry `pre-rollback` too.

### The assertion worth writing first

`examples/helm/README.md` records that `Release` has **no `helm rollback` verb**:
reverting values and re-applying fires `pre-upgrade` hooks, not `pre-rollback`
ones. That is a claim in a README and nothing checks it.

velero is already in the set and already carries `pre-rollback` — its hook is a
CRD-migration Job, which is exactly the case where firing the wrong event
matters. Making that assertion real needs no new chart, and it closes the more
important half of this gap. A post-rollback fixture is worth adding after it,
not before.

## Charts deliberately not here

**ambassador** — was the only chart in the sample declaring a `crd-install`
hook, and ships `apiextensions.k8s.io/v1beta1` CRDs. Kubernetes removed that API
in 1.22, so it fails on any current cluster with "no matches for kind
CustomResourceDefinition in version apiextensions.k8s.io/v1beta1" before formae
is involved at all. cert-manager carries the `crd-install` trait instead, with
`crds.enabled: true`.

**kubedb** — requires a licence key (`global.license`), so it cannot install at
all here. It was one of only two charts in the sample with `pre-rollback` hooks;
velero, the other, carries that trait now.

**keycloak-config-cli** — exists to configure a running Keycloak, and its
post-install hook waits for one. With no Keycloak to talk to the hook burns the
timeout every time. vela-core carries `post-rollback` instead.

**flowise**, **helm-dashboard** — their version pairs cross an immutable
field. flowise 5.1.1 -> 6.0.0 changes the Deployment's `spec.selector`, and
helm-dashboard's rollback cannot revert a PersistentVolumeClaim
(`spec is immutable after creation`). Kubernetes forbids both, under plain helm
as much as under formae, so the cell measures the chart's version history rather
than anything about adoption.

**openbao**, **portainer** — too slow to be worth a slot: openbao never settled
in eighteen minutes, portainer's rollback timed out. A chart earns its place by
exercising the release lifecycle, not by making the suite wait.

**stackgres-operator** — declares `kubeVersion: 1.18.0-0 - 1.35.x`, and CI runs
Kubernetes 1.36. It installs locally on 1.33 and cannot install on the version
that gates the branch, which is the version that matters.

**vela-core** — installs a validating webhook served by the release being
upgraded, so it denies its own upgrade:
`validating.core.oam-dev.v1beta1.componentdefinitions denied the request:
builtin package "vela/helm" undefined`. The same upgrade fails under plain helm.
Disabling the webhook made it pass, but a chart that only works with its own
admission control switched off is not testing much.

**connaisseur** — an image-signature admission controller whose default policy is
`static deny *:*`. It installs, then denies the image pull of everything after
it, including its own upgrade. It cost a whole sweep once: it failed, its state
was preserved for diagnosis, and its webhook then denied fourteen unrelated
charts, all reported as "does not install bare". Teardown no longer leaves a
failed release running, but connaisseur still cannot pass its own upgrade
without a permissive policy in values. Add it back with one if you want the
coverage.

**bitnami/\*** — Bitnami restricted its public image catalog in August 2025, so
these render but fail to pull.
