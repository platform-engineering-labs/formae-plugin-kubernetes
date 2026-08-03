# Adopt a Helm-installed release, then roll it back with Helm

A release created by `helm install` — nothing to do with formae — is discovered,
brought under management, upgraded by formae, and then rolled back from the Helm
side.

## Run it

```bash
make helm-adopt-test
```

Needs a reachable cluster, `make install` done, a running agent, and `helm`,
`kubectl`, `python3` on PATH. About four minutes — four kratos rollouts.

## What it does

| Step | Action | Result |
|------|--------|--------|
| 1 | `helm install --version 0.62.1` | revision 1, **no** formae ownership marker |
| 2 | `formae apply` | **refused** — formae did not install this release |
| 3 | discovery | appears as unmanaged `K8S::Helm::Release` |
| 4 | `formae extract` | a forma describing the live release |
| 5 | `formae apply --mode patch` | adopted; arrives as `update`, **revision unchanged** |
| 6 | repin to 0.63.0 + add `repoURL`, apply | revision 2 |
| 7 | `helm rollback 1` | revision 3, back at 0.62.1 |
| 8 | agent sync | formae reports 0.62.1 — the rollback is ordinary drift |

Real `helm history` from step 8:

```
REVISION  STATUS      CHART          DESCRIPTION
1         superseded  kratos-0.62.1  Install complete
2         superseded  kratos-0.63.0  Upgrade complete
3         deployed    kratos-0.62.1  Rollback to 1
```

## The refusal in step 2

```
release formae-helm-adopt/kratos already exists at revision 1 and was not
created by formae: applying would overwrite that record and destroy its
rollback history. Adopt it first with
`formae extract --query 'type:K8S::Helm::Release managed:false'`,
or choose a different metadata.name
```

The plugin stamps `formae.dev/managed=true` as a Helm **release label** on every
release it installs. Helm carries release labels forward — `upgrade` merges the
previous release's labels under the new ones, `rollback` copies them — so the
marker identifies a release *lineage* rather than a single revision.

Its absence is what makes step 2 a refusal instead of a takeover. Overwriting a
foreign release's record would rewrite its history, and `helm rollback` would then
roll back to revisions no forma ever described.

The marker is also why a **failed** formae install can be retried: formae withholds
the NativeID until a release is fully deployed, so a retry arrives as a `create`
against an existing release. The marker separates our own abandoned attempt, which
we may take over, from a genuinely foreign release, which we may not.

## Five things worth knowing before you adopt anything

**Adopt with `--mode patch`, never `reconcile`.** The extracted forma describes
only the release. A reconcile treats everything else already on that stack — here
the namespace — as absent from the forma and deletes it, taking the release down
with it. That is reconcile working as documented; it is just not what you want
mid-adoption.

**Adoption binds by resource label.** `formae extract` labels the release after its
native id (`<namespace>/<releaseName>`), adding a numeric suffix when state already
holds that label. Relabel it and formae treats it as a brand-new resource, issues a
`create`, and the ownership guard correctly refuses — adoption never completes.
That is why the demo drives everything after step 4 through the *extracted* forma
rather than through `kratos.pkl`.

**Adopting needs no `repoURL`; upgrading afterwards does.** Helm stores the whole
chart — templates included — in the release record, which is how `helm rollback`
re-renders offline. So when a forma pins the version already deployed, the plugin
reuses the stored chart and never fetches. That is the only reason adoption works
at all, because Helm does not record which repository a release came from and
`Read` cannot reconstruct it.

The moment you bump the version, a chart the cluster has never seen has to be
fetched, and `repoURL` becomes required — step 6 adds it. The plugin says so
plainly rather than letting Helm fail with "non-absolute URLs should be in form of
repo_name/path_to_chart". `oci://` references are self-describing and need nothing.

**Adoption is a pure bind and does not move the release.** The extracted forma
describes the live release exactly, so the patch carries no change and Helm is
never called. That matters for charts with `pre-upgrade` hooks — being adopted
does not run them.

**Which revision you roll back to decides whether the ownership marker comes
along.** Helm copies labels from the revision being restored. Rolling back to a
pre-adoption revision (as step 7 does) lands on one that never had the marker, so
the new revision does not either. Harmless: the guard only applies to a `create`,
and formae holds a NativeID for an adopted release, so every later operation is an
`update`.

## A formae bug the script works around

`formae extract` emits map keys as bare Pkl identifiers. Kratos names its identity
schemas after files, so the generated forma contains:

```pkl
identity.default.schema.json = "{...}"
```

which is not valid Pkl — a key that is not a plain identifier has to be
bracket-quoted. The script rewrites it. Any chart whose values contain a dotted
key hits this.

## Files

- `kratos.pkl` — declares the release at version B. Used for step 2's refusal and
  to create the target, stack and namespace. Not what performs the adoption.
- `helm-values.yaml` — values for step 1's `helm install`. Kept identical to the
  forma's `values` block so the demo measures one change at a time.
- `PklProject` — self-contained; see the note in it about why `examples/PklProject`
  cannot be used with `formae apply`.
