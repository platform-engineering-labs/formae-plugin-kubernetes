# Bookstore

Full-stack bookstore webapp on a managed Kubernetes cluster. Pick a cloud at
apply time with `--provider`. The same forma file runs on AWS, Azure, GCP,
OCI, or any kubeconfig-accessible cluster.

## What You Get

**Workload (all providers):**
- Namespace `bookstore`
- ConfigMaps for nginx config + index.html, backend env config
- Secret with DB credentials (placeholder — change for real use)
- ServiceAccount for the backend
- Frontend Deployment (nginx, 2 replicas) + LoadBalancer Service
- Backend Deployment (Node.js API, 2 replicas) + ClusterIP Service

**Cluster (per provider):**

| Provider | Infra |
|----------|-------|
| `aws`    | VPC + 2 public subnets + IGW + route table + security groups + IAM roles + EKS AutoMode cluster |
| `azure`  | Resource group + VNet + subnet + AKS cluster (1 system node) + RBAC role assignment |
| `gcp`    | VPC + subnet + Standard zonal GKE cluster (private nodes, public endpoint) |
| `oci`    | VCN + public/private subnets + IGW + NAT GW + Service GW + route tables + security list + OKE cluster + node pool |
| `local`  | None — uses your current kubectl context (OrbStack, kind, minikube, etc.) |

## Prerequisites

| Provider | CLI auth | Plugin |
|----------|----------|--------|
| `aws`    | `aws configure` (region defaults to `us-west-2`) | `formae-plugin-aws` |
| `azure`  | `az login` + set `AZURE_SUBSCRIPTION_ID` and `AZURE_PRINCIPAL_ID` (your AAD object id: `az ad signed-in-user show --query id -o tsv`) | `formae-plugin-azure` |
| `gcp`    | `gcloud auth application-default login`; `GCP_PROJECT=<your-project>` | `formae-plugin-gcp` |
| `oci`    | `oci session authenticate`; `OCI_COMPARTMENT_ID=<ocid>` (and friends, see Configuration) | `formae-plugin-oci` |
| `local`  | kubectl configured (`kubectl config current-context` returns your target) | none |

`formae-plugin-k8s` is required for all providers.

## Configuration

Declared CLI flags (auto-generated from `formae.Prop` declarations):

| Flag | Type | Default | Notes |
|------|------|---------|-------|
| `--provider` | string | `$FORMAE_PROVIDER` or `aws` | One of `aws`, `azure`, `gcp`, `oci`, `local`. Invalid values fail fast. |

Cluster-side knobs live in the `clusters/<provider>/vars.pkl` (or
`clusters/aws/bundle.pkl` for AWS) and are *not* exposed as CLI flags —
override them either by setting the env vars below, or by editing the
`vars.pkl` file directly.

**Env vars supported by default:**

| Provider | Env var | What it sets |
|----------|---------|--------------|
| `azure`  | `AZURE_SUBSCRIPTION_ID`, `AZURE_PRINCIPAL_ID` | subscription + AAD object id for the RBAC assignment |
| `gcp`    | `GCP_PROJECT` (or `GOOGLE_CLOUD_PROJECT`) | GCP project for VPC + GKE |
| `oci`    | `OCI_COMPARTMENT_ID`, `OCI_AVAILABILITY_DOMAIN`, `OCI_NODE_IMAGE_ID`, `OCI_SERVICE_GATEWAY_SERVICE_ID` | required OCI ids |
| `local`  | `K8S_CONTEXT` | kubectl context to target |

For knobs without env-var support (AWS `region`/`vpcCidr`/etc., GCP
`region`/`zone`/CIDRs, Azure `location`/`vnetCidr`/etc.), edit the
relevant `vars.pkl`.

## Deploy

```bash
# Local cluster (OrbStack / kind / minikube)
formae apply --mode reconcile --yes --watch \
  --provider local \
  examples/bookstore/main.pkl

# AWS EKS
formae apply --mode reconcile --yes --watch \
  --provider aws \
  examples/bookstore/main.pkl

# Azure AKS
formae apply --mode reconcile --yes --watch \
  --provider azure \
  examples/bookstore/main.pkl

# GCP GKE
GCP_PROJECT=my-gcp-project \
  formae apply --mode reconcile --yes --watch \
  --provider gcp examples/bookstore/main.pkl

# Oracle OKE
OCI_COMPARTMENT_ID=ocid1.compartment.oc1..xxx \
  formae apply --mode reconcile --yes --watch \
  --provider oci examples/bookstore/main.pkl
```

Cluster spin-up: ~5 min (GKE) to ~15 min (EKS). `local` is seconds.

## Smoke Test

After apply succeeds, verify the workload:

```bash
# Check pods are running
kubectl -n bookstore get pods

# Front the frontend service locally and hit it
kubectl -n bookstore port-forward svc/bookstore-frontend 8080:80
open http://localhost:8080

# Or get the LoadBalancer hostname/IP (cloud providers only)
kubectl -n bookstore get svc bookstore-frontend
```

The frontend page calls the backend `/api` and renders the JSON response.

## Tear Down

```bash
formae destroy --yes --provider <p> examples/bookstore/main.pkl
```

This removes the workload AND the cluster + supporting cloud infra. Tear-down
takes roughly the same time as deploy.

## Architecture

```
formae.Stack: k8s-bookstore
│
├── Cloud target  (aws.Config / gcp.Config / azure.Config / oci.Config)
├── Cloud infra   (VPC, subnets, IAM roles, security groups, ...)
├── Managed cluster (EKS / AKS / GKE / OKE) ── or none for `local`
│
└── K8S target  (EKSAuth / AKSAuth / GKEAuth / OCIAuth / KubeconfigAuth)
    └── Namespace: bookstore
        ├── ConfigMap: bookstore-frontend-config
        ├── ConfigMap: bookstore-backend-config
        ├── Secret:    bookstore-db-credentials
        ├── ServiceAccount: bookstore-backend
        ├── Deployment + Service: bookstore-frontend (nginx, LoadBalancer)
        └── Deployment + Service: bookstore-backend  (Node.js, ClusterIP)
```

## Troubleshooting

| Symptom | Cause / Fix |
|---------|-------------|
| `Expected value of type "aws"\|"azure"\|...` | Typo in `--provider`. Allowed: `aws`, `azure`, `gcp`, `oci`, `local`. |
| AKS apply fails on RBAC step | `AZURE_PRINCIPAL_ID` is unset or wrong. Run `az ad signed-in-user show --query id -o tsv` and export it. |
| GKE apply hangs / fails on cluster create | Confirm your project has the Container Engine API enabled; check `compute.vmExternalIpAccess` org policy. The example uses private nodes + private Google access by default. |
| OKE apply fails immediately | Default `OCI_NODE_IMAGE_ID` is region-specific. Look up an OKE-optimized image for your region: `oci ce node-pool-options get --node-pool-option-id all --compartment-id <id>` and export it. |
| `kubectl get pods` returns "couldn't get current server API group list" | Cluster credentials not configured. Run the cloud-specific `update-kubeconfig` / `get-credentials` command for your provider. |
