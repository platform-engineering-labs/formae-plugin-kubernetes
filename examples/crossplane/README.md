# Crossplane

Crossplane control plane on a managed Kubernetes cluster. Pick a cloud at
apply time by choosing the matching entry file — `examples/crossplane/<cloud>.pkl`,
where `<cloud>` is one of `aws`, `azure`, `gcp`, `oci`, or `local`. The same
workload runs on AWS, Azure, GCP, OCI, or any kubeconfig-accessible cluster.

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

The cluster module is `examples/clusters/<cloud>.pkl`. Cluster-side knobs
(region, CIDRs, k8s version, etc.) are overridable via `--prop <name>=<value>`
— see the bookstore [Configuration](../bookstore/README.md#configuration) for
the env-var support matrix.

## Deploy

```bash
# Local cluster
formae apply --mode reconcile --yes --watch \
  examples/crossplane/local.pkl

# AWS EKS
formae apply --mode reconcile --yes --watch \
  examples/crossplane/aws.pkl

# Azure AKS
formae apply --mode reconcile --yes --watch \
  examples/crossplane/azure.pkl

# GCP GKE
GCP_PROJECT=my-gcp-project \
  formae apply --mode reconcile --yes --watch \
  examples/crossplane/gcp.pkl

# Oracle OKE
OCI_COMPARTMENT_ID=ocid1.compartment.oc1..xxx \
  formae apply --mode reconcile --yes --watch \
  examples/crossplane/oci.pkl
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
formae destroy --on-dependents=cascade --yes examples/crossplane/<cloud>.pkl
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
