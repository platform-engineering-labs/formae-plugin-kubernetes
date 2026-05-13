# Clusters

Per-cloud Kubernetes cluster bundles. Each provider exposes the same shape — a
`Listing<formae.Resource>` of cloud-side infra (VPC, IAM, the cluster itself,
nodes) plus a `formae.Target` configured for the K8s plugin against that
cluster. The workload entries (`bookstore`, `crossplane`, `lgtm`, `target`)
pick one via `dispatch.For(<provider>)` based on the `--provider` flag.

## Available providers

| Provider | Bundle                  | Infra |
|----------|-------------------------|-------|
| `aws`    | `aws/bundle.pkl`        | VPC + subnets + IGW + security groups + IAM + EKS AutoMode cluster |
| `azure`  | `azure/bundle.pkl`      | Resource group + VNet + subnet + AKS cluster + RBAC role assignment |
| `gcp`    | `gcp/bundle.pkl`        | VPC + subnet + Standard zonal GKE cluster |
| `oci`    | `oci/bundle.pkl`        | VCN + public/private subnets + IGW + NAT GW + Service GW + OKE cluster + node pool |
| `local`  | `local/bundle.pkl`      | None. Uses your current kubectl context |

## Adding a new provider

1. Create `clusters/<provider>/bundle.pkl` exposing two top-level properties:

   ```pkl
   resources: Listing<formae.Resource>
   target: formae.Target
   ```

2. Add `<provider>` to the `Provider` typealias in `dispatch.pkl`.
3. Add a branch to `For(...)` returning a `Bundle` built from the new module.

Pkl is lazy, so only the chosen provider's resources get instantiated. A typo
like `--provider awz` fails at evaluation with a real Pkl type error — no
runtime plumbing required.
