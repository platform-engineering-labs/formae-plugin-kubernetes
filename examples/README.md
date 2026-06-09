# Examples

End-to-end examples deploying real workloads to Kubernetes using formae and
the K8S plugin. Each workload is a **directory of per-cloud entry files** —
pick AWS, Azure, GCP, OCI, or a local kubeconfig-accessible cluster by
applying the matching file (e.g. `examples/bookstore/aws.pkl`).

```
examples/
├── apps/                       # Reusable workload modules (target-parameterized)
│   ├── bookstore.pkl
│   ├── crossplane.pkl
│   └── lgtm/                   # Sub-modules: minio, loki, tempo, mimir, otel, grafana, demo
├── clusters/                   # Per-cloud managed-cluster modules
│   ├── aws.pkl                 # VPC, subnets, IAM, EKS
│   ├── azure.pkl               # Resource group, VNet, AKS, RBAC
│   ├── gcp.pkl                 # VPC, subnet, GKE
│   ├── oci.pkl                 # VCN, subnets, gateways, security list, OKE
│   └── local.pkl               # KubeconfigAuth (no provisioning)
├── bookstore/                  # Workload: frontend + backend webapp
│   ├── aws.pkl
│   ├── azure.pkl
│   ├── gcp.pkl
│   ├── oci.pkl
│   └── local.pkl
├── crossplane/                 # Workload: Crossplane control plane
│   ├── aws.pkl
│   ├── azure.pkl
│   ├── gcp.pkl
│   ├── oci.pkl
│   └── local.pkl
├── lgtm-observability/         # Workload: observability stack
│   ├── aws.pkl
│   ├── azure.pkl
│   ├── gcp.pkl
│   ├── oci.pkl
│   └── local.pkl
├── formations/                 # Native Pkl charts (no Helm)
└── helm/                       # Helm bridge examples
```

## Resolving Pkl deps

Every subdirectory ships its own `PklProject` declaring the Pkl deps it
needs (`@formae`, `@k8s`, optional cloud plugins, `@apps`, `@clusters`).
Helm wrappers live under `@k8s/helm/v<X.Y>/`, so no separate dep is
required.

**Before evaluating any example, resolve its Pkl deps.** `PklProject.deps.json`
is git-ignored and must be regenerated on a fresh clone (and any time a
`PklProject` changes). `pkl project resolve` does not cascade into local
`import("...")` dependencies, so each project that participates in the
import chain must be resolved individually.

```bash
# Generate the versioned K8s schemas under schema/pkl/generated (one-time).
make install

# Resolve shared modules once — all workload entries consume these.
pkl project resolve examples/apps/
pkl project resolve examples/clusters/

# Resolve the workload entry you are about to run, e.g.:
pkl project resolve examples/bookstore/
pkl project resolve examples/crossplane/
pkl project resolve examples/lgtm-observability/

# Helm bridge examples need their own resolve (also pulls in ../../helm).
pkl project resolve examples/helm/
```

Each example file's doc-block lists the exact resolve commands it needs.

## How it fits together

Each workload entry file imports one `clusters/<cloud>.pkl` module — the cloud
is chosen by which file you apply, not by a flag. The module exposes two
functions taking a `slug`: `resources(slug)` returns everything needed to
provision the cluster (including its cloud-side target), and `target(slug)`
returns the Kubernetes target authenticated against the just-provisioned
cluster. The entry file calls `cluster.target("<workload>")` and spreads
`cluster.resources("<workload>")` alongside the workload's own resources in a
single `forma { }` block. The `slug` makes the managed-cluster name and both
target labels (cloud-side `<cloud>-target-<slug>` and K8s-side
`k8s-target-<cloud>-<slug>`) unique, so bookstore, crossplane, and
lgtm-observability can co-exist on one cloud account without colliding.
`local.pkl` provisions nothing — `resources` is empty and only a
`KubeconfigAuth` target is exposed.

## Workloads

| Example | Provisions | Reusable module |
|---------|------------|-----------------|
| [bookstore](bookstore/) | Namespace, ConfigMaps, Secret, ServiceAccount, two Deployments + Services | `apps/bookstore.pkl` |
| [crossplane](crossplane/) | Namespace, RBAC, Crossplane core Deployment + Service | `apps/crossplane.pkl` |
| [lgtm-observability](lgtm-observability/) | MinIO + Loki + Tempo + Mimir + OTel + Grafana (~25 pods) | `apps/lgtm/lgtm.pkl` |

Each workload has a per-directory README with prerequisites, exact deploy
commands per cloud, smoke test, and tear-down.

## Providers

| Provider | Cluster | Required setup |
|----------|---------|----------------|
| `aws`    | EKS AutoMode (VPC + IAM + cluster) | `aws configure` |
| `azure`  | AKS (RG + VNet + cluster + RBAC role assignment) | `az login`; `AZURE_SUBSCRIPTION_ID`, `AZURE_PRINCIPAL_ID` env vars |
| `gcp`    | Standard zonal GKE (VPC + subnet + private-node cluster) | `gcloud auth application-default login`; set `GCP_PROJECT=...` |
| `oci`    | OKE (VCN + subnets + gateways + security + cluster + node pool) | `oci session authenticate`; set `OCI_COMPARTMENT_ID=ocid1...` |
| `local`  | None — uses your current kubectl context | kubectl configured locally |

Cluster-side knobs (region, CIDRs, k8s version, etc.) live in
`examples/clusters/<cloud>.pkl` and remain overridable at apply time via
`--prop <name>=<value>`. A subset also have env-var fallbacks.

## Adding a new provider

1. Create `examples/clusters/<provider>.pkl` as a plain Pkl module exposing
   `resources(slug: String): Listing` and `target(slug: String): formae.Target`.
2. Add a `<provider>.pkl` entry file in each workload directory
   (`bookstore/`, `crossplane/`, `lgtm-observability/`) that imports the new
   cluster module and calls `cluster.resources(...)` / `cluster.target(...)`.
3. Add the provider's plugin to `examples/clusters/PklProject` and to each
   workload's `PklProject` under `dependencies`.

## Formations and Helm

See [formations/](formations/) and [helm/](helm/) for deploying workloads via
native Pkl charts or the Helm bridge. These predate the shared `apps/` pattern
and will be migrated over time.
