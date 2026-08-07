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
