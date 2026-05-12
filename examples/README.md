# Examples

End-to-end examples deploying real workloads to Kubernetes using formae and
the K8S plugin. Each workload entry is **one forma file that runs on any
provider** — pick AWS, Azure, GCP, OCI, or a local kubeconfig-accessible
cluster at apply time with `--provider`.

```
examples/
├── apps/                       # Reusable workload modules (target-parameterized)
│   ├── bookstore.pkl
│   ├── crossplane.pkl
│   └── lgtm/                   # Sub-modules: minio, loki, tempo, mimir, otel, grafana, demo
├── clusters/                   # Per-provider managed-cluster bundles
│   ├── dispatch.pkl            # Provider picker (typealias Provider, function For)
│   ├── aws/                    # VPC, subnets, IAM, EKS
│   ├── azure/                  # Resource group, VNet, AKS, RBAC
│   ├── gcp/                    # VPC, subnet, GKE
│   ├── oci/                    # VCN, subnets, gateways, security list, OKE
│   └── local/                  # KubeconfigAuth (no provisioning)
├── bookstore/                  # Workload entry: frontend + backend webapp
├── crossplane/                 # Workload entry: Crossplane control plane
├── lgtm/                       # Workload entry: observability stack
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
pkl project resolve examples/lgtm/

# Helm bridge examples need their own resolve (also pulls in ../../helm).
pkl project resolve examples/helm/
```

Each example file's doc-block lists the exact resolve commands it needs.

## How it fits together

Workload `main.pkl` files declare a `provider` `formae.Prop` and call
`dispatch.For(provider)` to pick a cluster bundle. The bundle exposes
`resources` (everything needed to provision the cluster, including its
cloud-side target) and `target` (the Kubernetes target authenticated against
the just-provisioned cluster). The workload's resources reference that K8S
target; the `forma { }` block spreads everything into a single deploy.

A typo like `--provider awz` is rejected at evaluation time by the
literal-union `Provider` typealias — no runtime throw, real Pkl type error
pointing at the allowed values.

## Workloads

| Example | Provisions | Reusable module |
|---------|------------|-----------------|
| [bookstore](bookstore/) | Namespace, ConfigMaps, Secret, ServiceAccount, two Deployments + Services | `apps/bookstore.pkl` |
| [crossplane](crossplane/) | Namespace, RBAC, Crossplane core Deployment + Service | `apps/crossplane.pkl` |
| [lgtm](lgtm/) | MinIO + Loki + Tempo + Mimir + OTel + Grafana (~25 pods) | `apps/lgtm/lgtm.pkl` |

Each workload has a per-directory README with prerequisites, exact deploy
commands per provider, smoke test, and tear-down.

## Providers

| Provider | Cluster | Required setup |
|----------|---------|----------------|
| `aws`    | EKS AutoMode (VPC + IAM + cluster) | `aws configure` |
| `azure`  | AKS (RG + VNet + cluster + RBAC role assignment) | `az login`; `AZURE_SUBSCRIPTION_ID`, `AZURE_PRINCIPAL_ID` env vars |
| `gcp`    | Standard zonal GKE (VPC + subnet + private-node cluster) | `gcloud auth application-default login`; set `GCP_PROJECT=...` |
| `oci`    | OKE (VCN + subnets + gateways + security + cluster + node pool) | `oci session authenticate`; set `OCI_COMPARTMENT_ID=ocid1...` |
| `local`  | None — uses your current kubectl context | kubectl configured locally |

Cluster-side knobs (region, cidrs, cluster name, etc.) live in
`examples/clusters/<provider>/vars.pkl` (or `clusters/aws/bundle.pkl`).
A subset have env-var fallbacks; for everything else, edit the
`vars.pkl` file directly.

## Adding a new provider

1. Create `examples/clusters/<provider>/bundle.pkl` exposing top-level
   `resources: Listing` and `target: formae.Target`.
2. Add `<provider>` to the `Provider` typealias in
   `examples/clusters/dispatch.pkl`.
3. Add an `else if` branch to `dispatch.For` returning the new bundle.
4. Add the provider's plugin to `examples/clusters/PklProject` and to each
   workload's `PklProject` under `dependencies`.

## Formations and Helm

See [formations/](formations/) and [helm/](helm/) for deploying workloads via
native Pkl charts or the Helm bridge. These predate the shared `apps/` pattern
and will be migrated over time.
