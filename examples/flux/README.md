# Flux — installed end-to-end with formae (Helm + catch-all)

Installs the **entire Flux operator** with a single `formae apply` — no kubectl,
no `flux install`. Demonstrates that `HelmChart` + the `K8S::Custom::Resource`
catch-all cover a real operator chart: controllers/RBAC/Services map to typed
resources, and the 14 CRDs the chart ships fall back to the catch-all.

## How it works

`flux-helm.pkl` renders `fluxcd-community/flux2` via `HelmChart` at pkl-eval
time. Each rendered object is dispatched:
- **typed kinds** (Deployment, ServiceAccount, ClusterRole, Service, Job,
  NetworkPolicy, Namespace) → typed formae resources,
- **unsupported kinds** (the 14 `CustomResourceDefinition`s) → `K8S::Custom::Resource`
  via the `dispatch.mapCustom` fallback.

One `formae apply` then creates all ~40 resources.

## Prerequisites (tooling only)

```bash
helm repo add fluxcd-community https://fluxcd-community.github.io/helm-charts
helm repo update
# pkl-reader-helm must be on PATH (formae registers it automatically)
```

## Install Flux

```bash
pkl project resolve examples/flux
formae apply --mode reconcile --yes --watch examples/flux/flux-helm.pkl
```

Verify:
```bash
kubectl get pods -n flux-system
kubectl get crd | grep fluxcd.io                       # 14 CRDs
formae inventory resources --query "stack:flux managed:true" --max-results 100
#   → typed controllers/RBAC + 14 K8S::Custom::Resource (the CRDs)
```

## Manage Flux custom resources

Once Flux is installed (CRDs present), manage its CRs through the catch-all —
see `gitrepository.pkl` (a `GitRepository` pointing at podinfo). The target's
`customResourceGroups` allowlist makes these discoverable.

```bash
formae apply --mode reconcile --yes examples/flux/gitrepository.pkl
```

## Teardown

```bash
formae destroy --yes examples/flux/flux-helm.pkl
```

## Notes
- Bump the `@k8s/v1.33/...` imports + `kubernetesVersion` to match your cluster minor.
- The chart ships its CRDs in `templates/` (not `crds/`), so they render without
  `--include-crds`.
