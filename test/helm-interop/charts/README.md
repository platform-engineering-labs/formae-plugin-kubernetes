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
