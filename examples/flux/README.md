# Flux — installed end-to-end with formae (Helm release + catch-all)

Installs the **entire Flux operator** with a single `formae apply` — no kubectl,
no `flux install`. Then manages Flux's own custom resources through the
`K8S::Custom::Resource` catch-all, which is the half of the example that needs
the CRDs the chart installs.

## How it works

`flux-helm.pkl` declares `fluxcd-community/flux2` as one `K8S::Helm::Release`.
Helm installs the chart's ~40 objects — controllers, RBAC, Services and the 14
`CustomResourceDefinition`s — in its own order, and formae manages the release.
The objects are collapsed under it in discovery and listed on the release's
`resourceNames`.

## Prerequisites

None beyond a reachable cluster. The plugin embeds the Helm SDK, so there is no
`helm repo add` and no reader binary on `PATH`.

## Install Flux

```bash
pkl project resolve examples/flux
formae apply --mode reconcile --yes examples/flux/flux-helm.pkl
```

Verify:
```bash
kubectl get pods -n flux-system
kubectl get crd | grep fluxcd.io                       # 14 CRDs
helm list -n flux-system                               # the release formae created
formae inventory resources --query "stack:flux managed:true" --max-results 100
#   → one K8S::Helm::Release plus the Namespace, not ~40 objects
```

## Manage Flux custom resources

Once Flux is installed (CRDs present), manage its CRs through the catch-all —
see `gitrepository.pkl` (a `GitRepository` pointing at podinfo).

```bash
formae apply --mode reconcile --yes examples/flux/gitrepository.pkl
```

## Teardown

```bash
formae destroy --yes examples/flux/flux-helm.pkl
```

## Notes
- Bump the `@k8s/v1.33/...` imports + `kubernetesVersion` to match your cluster
  minor.
- The chart ships its CRDs in `templates/` (not `crds/`), so a `formae destroy`
  removes them with the release rather than leaving them behind.
