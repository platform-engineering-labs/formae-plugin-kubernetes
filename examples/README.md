# Examples

End-to-end examples deploying real workloads to Kubernetes using formae and
the K8s plugin. Each subdirectory targets a different deployment scenario —
managed cloud K8s (EKS, GKE, AKS, OKE), local or self-managed clusters
(`vanilla/`), or chart-based deploys via the `formae-helm` Pkl wrapper or
hand-written PKL charts.

## Layout

```
examples/
├── apps/                      # Shared, reusable workload modules
├── helm/                      # Helm charts via @formae-helm wrappers
├── charts/                    # Hand-written PKL charts (no Helm needed)
├── vanilla/                   # Any kubeconfig-reachable cluster
├── eks/                       # AWS EKS (cross-cloud, single forma)
├── eks-full-stack/            # EKS + IAM + VPC + workloads end-to-end
├── gke/                       # GCP GKE Autopilot
├── aks/                       # Azure AKS
├── oke/                       # Oracle OKE
├── orbstack-target.pkl        # Snippet: KubeconfigAuth for OrbStack
├── PklProject.deps.json       # Resolved Pkl deps shared by sub-projects
└── README.md
```

Every subdirectory ships its own `PklProject` declaring the Pkl deps it
needs (`@formae`, `@k8s`, optional cloud plugins, `@formae-helm`). Run
`pkl project resolve <subdir>` once before evaluating a forma in it.

## How to read these examples

A typical forma in this directory has four parts:

1. **Imports** — pull in `@formae/forma.pkl`, the K8s plugin schema (or a
   versioned subtree under `@k8s/v<X.Y>/`), any cloud plugin, and shared
   workload modules from `@apps/`.
2. **Stack** — names the unit of work (`new formae.Stack { ... }`).
3. **Target** — declares a K8s cluster with auth and `kubernetesVersion`.
   For cloud providers the cluster is also created in the same forma.
4. **Resources** — typed K8s objects, either hand-written, spread from a
   shared `@apps/` module, or rendered from a Helm chart via `@formae-helm`.

## `apps/` — shared workload modules

Reusable workloads parameterised by the K8s target. The cloud-specific
forma files (eks, gke, aks, …) all import from `@apps/` so the workload
is written once and deployed everywhere.

| App | Description |
|---|---|
| `bookstore.pkl` | Frontend (nginx) + backend (Node.js API): ConfigMaps, Secrets, ServiceAccount, Deployments, Services |
| `crossplane.pkl` | Crossplane control plane (Namespace, RBAC, Deployment, Service); CRDs are installed by Crossplane itself at startup |

## `helm/` — Helm charts via `@formae-helm`

Renders a Helm chart at Pkl-eval time and maps the output to typed K8s
resources via the `@formae-helm/v<X.Y>/HelmChart.pkl` wrapper. Same
forma → reconcile → drift loop as hand-written resources.

| File | What it deploys | K8s wrapper |
|---|---|---|
| `nginx-v1.31.pkl` | bitnami/nginx, 2 replicas, ClusterIP service | v1.31 |
| `nginx-v1.34.pkl` | same, latest supported minor | v1.34 |
| `nginx.pkl` | nginx pinned to v1.34 (legacy unsuffixed name) | v1.34 |
| `memcached-v1.31.pkl` | bitnami/memcached standalone | v1.31 |
| `postgresql-v1.31.pkl` | bitnami/postgresql primary-only | v1.31 |

Prerequisites: `pkl-reader-helm` on `PATH`; `helm repo add bitnami
https://charts.bitnami.com/bitnami && helm repo update`. The
`kubernetesVersion` on the Target must match the `@formae-helm/v<X.Y>`
import (`v1.31` ↔ `v1.31`).

```bash
pkl project resolve examples/helm/
formae apply examples/helm/nginx-v1.31.pkl --mode reconcile --yes --watch
formae destroy examples/helm/nginx-v1.31.pkl --yes --watch
```

Drop a new chart by copying one of the existing `*-v1.<minor>.pkl` files
and updating `chart`, `version`, and `values`. See
[helm/README.md](../helm/README.md) for what the wrapper does under the
hood.

## `charts/` — hand-written PKL charts

Bypass Helm entirely. PKL renders the manifests directly using the K8s
schema types. Useful when a workload is small enough to express as native
PKL and you want type safety end-to-end without a `helm template` step.

| File | What it deploys |
|---|---|
| `nginx.pkl` | Minimal nginx deployment + service |
| `langfuse.pkl` | Langfuse self-hosted (DB + app) |

## `vanilla/` — any kubeconfig-reachable cluster

No cloud provider plugin required. Targets whatever `kubectl
config current-context` points at — kind, OrbStack, k3s, a remote
cluster, etc.

```bash
formae apply examples/vanilla/vanilla.pkl
kubectl -n bookstore port-forward svc/bookstore-frontend 8080:80
open http://localhost:8080
formae destroy examples/vanilla/vanilla.pkl
```

`vanilla/crossplane.pkl` deploys the Crossplane control plane the same
way; Crossplane installs its own CRDs on first start, so formae doesn't
manage them.

`orbstack-target.pkl` is a minimal Target snippet you can `import` from
your own forma when working with OrbStack's Kubernetes.

## `eks/`, `gke/`, `aks/`, `oke/` — managed cloud K8s

Each subdirectory provisions a managed cluster (VPC, IAM, control plane)
and deploys the bookstore in a single forma. No manual `kubeconfig`
plumbing — provider-agnostic auth handles the token refresh
(STS / OAuth2 / Azure AD / OCI signed requests).

Prerequisites per provider:
- **eks** — AWS credentials, `formae-plugin-aws` installed.
- **gke** — `gcloud auth application-default login`, `formae-plugin-gcp` installed.
- **aks** — Azure credentials, `formae-plugin-azure` installed.
- **oke** — OCI credentials, `formae-plugin-oci` installed.

```bash
# EKS
formae apply --mode reconcile --yes --watch examples/eks/eks.pkl

# GKE
formae apply --mode reconcile --yes --watch \
  --prop project=my-gcp-project examples/gke/gke.pkl

# AKS
formae apply --mode reconcile --yes --watch examples/aks/aks.pkl

# OKE
formae apply --mode reconcile --yes --watch examples/oke/oke.pkl
```

The frontend service gets a cloud-native LoadBalancer; use the printed
external IP / hostname to reach it.

`eks-full-stack/` is a heavier EKS example that wires IAM Roles for
Service Accounts, AWS Load Balancer Controller, and additional infra
beyond the minimum cluster.

## Adding a new cloud provider example

1. Create `examples/<provider>/`.
2. Add a `PklProject` with deps on `@formae`, `@k8s`, `@<cloud>`, and `@apps`.
3. Write `<provider>.pkl` that:
   - Provisions cloud infrastructure (cluster + networking).
   - Creates a K8s `Target` with the provider's auth class
     (`GKEAuth`, `EKSAuth`, `AKSAuth`, `OKEAuth`).
   - Sets `kubernetesVersion` on the `Config` to match the cluster minor.
   - Imports `@apps/bookstore.pkl` and spreads
     `bookstore.allResources(target)` into the `forma {}` block.
4. The bookstore module handles all K8s resources — you only write the
   infra + auth.
