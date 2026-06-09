# Clusters

Per-cloud Kubernetes cluster modules. Each provider exposes the same shape — two
functions, `resources(slug: String): Listing` returning cloud-side infra (VPC,
IAM, the cluster itself, nodes) and `target(slug: String): formae.Target`
configured for the K8s plugin against that cluster. A workload entry
(`examples/<workload>/<cloud>.pkl`) imports one module and calls
`cluster.resources("<workload>")` / `cluster.target("<workload>")`. The `slug`
argument makes the managed-cluster name and both target labels unique, so
multiple workloads can co-exist on one cloud account without colliding.

## Available providers

| Provider | Module       | Infra |
|----------|--------------|-------|
| `aws`    | `aws.pkl`    | VPC + subnets + IGW + security groups + IAM + EKS AutoMode cluster |
| `azure`  | `azure.pkl`  | Resource group + VNet + subnet + AKS cluster + RBAC role assignment |
| `gcp`    | `gcp.pkl`    | VPC + subnet + Standard zonal GKE cluster |
| `oci`    | `oci.pkl`    | VCN + public/private subnets + IGW + NAT GW + Service GW + OKE cluster + node pool |
| `local`  | `local.pkl`  | None. Uses your current kubectl context |

## Adding a new provider

1. Create `clusters/<provider>.pkl` exposing two functions:

   ```pkl
   function resources(slug: String): Listing
   function target(slug: String): formae.Target
   ```

2. Add a `<provider>.pkl` entry file in each workload directory
   (`examples/<workload>/<provider>.pkl`) that imports the new module and calls
   `cluster.resources(...)` / `cluster.target(...)`.

Pkl is lazy, so only the chosen file's resources get instantiated. Picking a
provider is just picking a file path on the `formae apply` command line — a typo
fails fast with a real Pkl error, no runtime plumbing required.
