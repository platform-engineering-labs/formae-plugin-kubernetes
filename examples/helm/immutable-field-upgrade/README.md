# A chart version that changes an immutable field

Some upgrades cannot be performed. Not by formae, not by Helm, not by anything —
and it is worth seeing that directly, because the failure surfaces through formae
and looks like formae's fault.

## Run it

```bash
make helm-immutable-test
```

Needs a reachable cluster, `make install` done, a running agent, and `helm` and
`kubectl` on PATH. About three minutes.

## What it does

| Step | Action | Result |
|------|--------|--------|
| 1 | `formae apply` at 5.1.1 | installed, revision 1 |
| 2 | repin to 6.0.0, `formae apply` | upgrade fails — `spec.selector: field is immutable` |
| 3 | release inspected | revision 2 exists and is **failed**; 5.1.1 still serving |
| 4 | plain `helm upgrade` to 6.0.0 | **same error**, no formae involved |
| 5 | `helm upgrade --force-replace --server-side=false` | **same error again** |

## A failed upgrade still writes a revision

Worth knowing before reading `helm list`, because it misleads here. A failed
upgrade leaves revision 2 recorded at the new chart version while revision 1
stays the one actually deployed. `helm list` shows the newest revision, so it
reports 6.0.0 and looks like the upgrade worked. `helm history` tells the truth:

```
REVISION  STATUS      CHART            DESCRIPTION
1         deployed    flowise-5.1.1    Install complete
2         failed      flowise-6.0.0    Upgrade "flowise" failed: ... immutable
```

The script asserts on the newest **deployed** revision for exactly this reason.

## Why

Kubernetes freezes some fields once an object exists. `Deployment.spec.selector`
is one; most of `PersistentVolumeClaim.spec` and `Service.spec.clusterIP` are
others. Helm upgrades by **patching** what is already there, so when a chart
changes one of those between versions the apiserver rejects the patch:

```
Deployment.apps "flowise" is invalid: spec.selector: Invalid value:
  v1.LabelSelector{MatchLabels:map[string]string{
    "app.kubernetes.io/component":"flowise", ...}}: field is immutable
```

flowise 6.0.0 renames its selector labels. That is a legitimate thing to do in a
major version, and it makes the upgrade impossible in place.

## Why `--force` does not save you

The instinct is that Helm must have a flag for this. It has one, and it does not
help:

```
helm upgrade ... --force-replace --server-side=false
Error: UPGRADE FAILED: failed to replace object: Deployment.apps "flowise"
  is invalid: spec.selector: ...: field is immutable
```

`--force-replace` issues a **replace** — a PUT over the existing object. A
replace still cannot change an immutable field. What would work is deleting the
Deployment and letting the chart recreate it, and no `helm upgrade` will do that
for you at any flag combination.

Two Helm 4 details worth knowing while you are here:

- the flag is `--force-replace`; it was `--force` in Helm 3
- it refuses to run alongside server-side apply — `cannot use server-side apply
  and force replace together` — and server-side apply is the Helm 4 default, so
  `--server-side=false` is required to even reach the error above

## What this means for `K8S::Helm::Release`

Nothing formae can route around. The release is one resource to formae; Helm owns
the objects inside it, so formae has no way to single out the Deployment and
replace it while patching the other twenty. It hands the upgrade to Helm and
inherits what Helm can do.

**It bites harder on rollback**, and that is the easier way to meet it by
accident: a rollback applies an *older* manifest, so if an immutable field moved
in the meantime the rollback is refused for the same reason. `helm-dashboard`
does exactly this over a PersistentVolumeClaim.

**Whether `HelmChart.pkl` escapes it is untested.** There formae owns each
rendered object, so in principle the changed field could be marked `createOnly`
and that one object replaced. That only holds if formae issues a genuine
destroy-then-create rather than a replace — a replace fails here just as Helm's
does. Nobody has checked.

## Recovering

Delete the object the chart cannot patch and re-apply:

```bash
kubectl delete deployment flowise -n formae-helm-immutable
formae apply --mode reconcile --yes flowise.pkl
```

Or uninstall and reinstall the release. Either loses whatever the object was
serving, which is the real cost and the reason this is not done for you.
