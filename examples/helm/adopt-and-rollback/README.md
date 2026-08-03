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
| 5 | `formae apply --mode patch` | adopted; arrives as `update`, revision 2 |
| 6 | repin to 0.63.0, apply | revision 3 |
| 7 | `helm rollback 2` | revision 4, back at 0.62.1 |
| 8 | agent sync | formae reports 0.62.1 — the rollback is ordinary drift |

Real `helm history` from step 8:

```
REVISION  STATUS      CHART          DESCRIPTION
1         superseded  kratos-0.62.1  Install complete
2         superseded  kratos-0.62.1  Upgrade complete
3         superseded  kratos-0.63.0  Upgrade complete
4         deployed    kratos-0.62.1  Rollback to 2
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
marker identifies a release *lineage*, not a single revision, and survives step 7.

Its absence is what makes step 2 a refusal instead of a takeover. Overwriting a
foreign release's record would rewrite its history, and `helm rollback` would then
roll back to revisions no forma ever described.

The marker is also why a **failed** formae install can be retried: formae withholds
the NativeID until a release is fully deployed, so a retry arrives as a `create`
against an existing release. The marker separates our own abandoned attempt, which
we may take over, from a genuinely foreign release, which we may not.

## Four things worth knowing before you adopt anything

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

**An extracted forma needs `repoURL` added by hand.** `repoURL` is optional in the
schema, but Helm's release record does not retain which repository a chart came
from, so `Read` cannot reconstruct it. An adopted HTTP-repo release therefore
arrives as `chart = "kratos"` with no repository, which nothing can resolve. The
plugin rejects that up front and names the fix. `oci://` references are
self-describing and unaffected — prefer them if you want adoption to round-trip
cleanly.

**Adoption performs one upgrade, it does not bind silently.** Because the extracted
forma has to gain that `repoURL`, the adopting patch carries a real change and the
release advances a revision (1 → 2 above). Charts with `pre-upgrade` hooks will run
them.

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
