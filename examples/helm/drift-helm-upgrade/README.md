# Deploy version A with formae, upgrade to version B with Helm

What happens when someone runs `helm upgrade` on a release formae manages?

Short answer: formae notices. A `K8S::Helm::Release` is not opaque to drift
detection — `Read` returns the chart version and the user-supplied values out of
the release record, so a Helm-side version bump surfaces as an ordinary field
diff on `version`, handled by the same machinery as any other resource.

## Run it

```bash
make helm-drift-test
```

Needs a reachable cluster, `make install` done, a running agent, and `helm` +
`kubectl` on PATH. Takes about two minutes — three kratos rollouts.

## What it does

| Step | Action | Result |
|------|--------|--------|
| 1 | `formae apply` | release at **0.62.1**, revision 1 |
| 2 | `helm upgrade --version 0.63.0` | release at **0.63.0**, revision 2 |
| 3 | agent sync → `Read` | formae reports **0.63.0** — drift is visible |
| 4 | `formae apply --mode reconcile` | **refused** |
| 5 | `formae apply --mode reconcile --force` | back to **0.62.1**, revision 3 |

Actual output from step 5:

```
REVISION  UPDATED                   STATUS      CHART          DESCRIPTION
1         Mon Aug  3 13:21:50 2026  superseded  kratos-0.62.1  Install complete
2         Mon Aug  3 13:22:13 2026  superseded  kratos-0.63.0  Upgrade complete
3         Mon Aug  3 13:22:54 2026  deployed    kratos-0.62.1  Upgrade complete
```

Every step is a real Helm revision. `helm history` and `helm rollback` keep
working throughout, because the release is a genuine Helm release rather than a
formae imitation of one.

## Step 4 is the interesting one

A plain reconcile does **not** silently overwrite the Helm-side change:

```
Error: forma rejected because the stacks it references have been modified
since the last reconcile command.
  1) use the '--force' flag to apply the forma anyway, or
  2) manually adjust your own code:
     formae extract --query='stack:helm-drift type:K8S::Helm::Release label:kratos' <file>
```

So you get an explicit choice between the two sane outcomes:

- **Revert** — the forma is the truth, `--force` pulls the release back to 0.62.1.
- **Absorb** — the Helm change was intentional, `formae extract` folds 0.63.0
  into the forma so the two agree without touching the cluster.

Neither happens by accident, which is the point.

## What this does not cover

**Drift *inside* the release is invisible.** `kubectl edit deployment/kratos`
changes nothing in the release record, so formae reports the release as in sync.
Helm does not detect that either — it is why `helm diff` exists as a plugin.
Closing that gap would mean diffing every rendered object against live state on
every read, which is the reimplement-Helm path this resource exists to avoid.

**Values drift is only detected at the top level of the diff.** `Read` returns
`rel.Config` — the user-supplied values, not merged with chart defaults — so
`helm upgrade --set` shows up. But `--reuse-values` (as this demo uses) carries
the previous values forward, so only `version` moves here.

## Files

- `kratos.pkl` — the forma, pinned at version A. Never edited during the demo.
- `PklProject` — self-contained on purpose. `examples/PklProject` declares nested
  local dependencies (`clusters`, `apps`, `formations`) and evaluating any forma
  through `formae apply` against it trips a Pkl bug:
  `Dependency$LocalDependency cannot be cast to ...$RemoteDependency`. Every
  example in this repo is affected, not just this one. A single local dependency
  on the plugin's own schema evaluates fine, which is all this project declares.
