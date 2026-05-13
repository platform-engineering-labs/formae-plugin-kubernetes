# Crossplane

Crossplane control plane on a managed Kubernetes cluster. Pick a cloud at
apply time with `--provider`. Same forma file works on AWS, Azure, GCP, OCI,
or any kubeconfig-accessible cluster.

Crossplane's init container installs its CRDs (Provider, Configuration,
Composition, ...) on first start — formae deliberately does NOT manage those
CRDs, since Crossplane mutates them at runtime and a drift-reconcile loop
would fight those mutations.

## What You Get

**Workload (all providers):**
- Namespace `crossplane-system`
- ServiceAccount + ClusterRole + ClusterRoleBinding (Crossplane core RBAC)
- Role + RoleBinding (leader election in `crossplane-system`)
- Deployment: `crossplane` (1 replica, includes init container that installs CRDs)
- Service: `crossplane` (metrics on 8080, webhooks on 9443)

**Cluster (per provider):** same as the [bookstore example](../bookstore/README.md#what-you-get).

## Prerequisites

Same as [bookstore](../bookstore/README.md#prerequisites). Each provider needs
its cloud CLI authenticated and the matching `formae-plugin-<provider>`
installed; `formae-plugin-kubernetes` is required for all providers.

## Configuration

Declared CLI flags (auto-generated from `formae.Prop` declarations):

| Flag | Type | Default | Notes |
|------|------|---------|-------|
| `--provider` | string | `$FORMAE_PROVIDER` or `aws` | One of `aws`, `azure`, `gcp`, `oci`, `local`. |

Cluster-side knobs live in `clusters/<provider>/vars.pkl` and are not
exposed as CLI flags — see the bookstore [Configuration](../bookstore/README.md#configuration)
for the env-var support matrix.

## Deploy

```bash
# Local cluster
formae apply --mode reconcile --yes --watch \
  --provider local \
  examples/crossplane/main.pkl

# AWS EKS
formae apply --mode reconcile --yes --watch \
  --provider aws \
  examples/crossplane/main.pkl

# Azure AKS
formae apply --mode reconcile --yes --watch \
  --provider azure \
  examples/crossplane/main.pkl

# GCP GKE
GCP_PROJECT=my-gcp-project \
  formae apply --mode reconcile --yes --watch \
  --provider gcp examples/crossplane/main.pkl

# Oracle OKE
OCI_COMPARTMENT_ID=ocid1.compartment.oc1..xxx \
  formae apply --mode reconcile --yes --watch \
  --provider oci examples/crossplane/main.pkl
```

## Smoke Test

```bash
# Verify the deployment is running
kubectl -n crossplane-system get pods

# Watch the logs (init container should report CRDs installed)
kubectl -n crossplane-system logs deploy/crossplane

# CRDs should be installed by the init container
kubectl get crds | grep crossplane.io
```

To install a Crossplane provider (managed by `kubectl`, not formae — providers
mutate at runtime):

```bash
kubectl apply -f - <<EOF
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-kubernetes
spec:
  package: xpkg.crossplane.io/crossplane-contrib/provider-kubernetes:v0.14.0
EOF
```

## Tear Down

```bash
formae destroy --yes --provider <p> examples/crossplane/main.pkl
```

Removes Crossplane core + the cluster + cloud infra.

## Architecture

```
formae.Stack: k8s-crossplane
│
├── Cloud target + cloud infra + managed cluster (provider-specific)
│
└── K8S target  (EKSAuth / AKSAuth / GKEAuth / OCIAuth / KubeconfigAuth)
    └── Namespace: crossplane-system
        ├── ServiceAccount: crossplane
        ├── ClusterRole + ClusterRoleBinding: crossplane
        ├── Role + RoleBinding: crossplane-leader-election
        ├── Deployment: crossplane (init container + main)
        └── Service: crossplane (metrics, webhooks)
```

## Troubleshooting

| Symptom | Cause / Fix |
|---------|-------------|
| Crossplane Pod stuck `Init` | Check init container logs — it bootstraps CRDs on first run. Apply may report success before init finishes; give it ~30s. |
| `Provider` CR rejected | CRDs not yet installed. Wait for the Crossplane Pod to be `Ready` then retry. |
| Same per-provider issues as bookstore | See [bookstore Troubleshooting](../bookstore/README.md#troubleshooting). |
