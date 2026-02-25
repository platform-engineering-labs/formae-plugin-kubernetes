# EKS Full-Stack Example

Two-stage deployment that provisions an EKS cluster on AWS and then deploys the bookstore webapp into it — all from a single local Formae instance with both AWS and K8S plugins.

```
┌─────────────────────────────────────────────────────────┐
│  Local machine (formae + AWS plugin + K8S plugin)       │
│                                                         │
│  Stage 1: formae apply stage1-eks.pkl ──► AWS           │
│           (VPC, subnets, IAM, EKS)       ~15 min        │
│                                                         │
│  Bridge:  aws eks update-kubeconfig      (manual)       │
│                                                         │
│  Stage 2: formae apply stage2-webapp.pkl ──► K8S (EKS)  │
│           (Namespace, Deployments, Services, Ingress)    │
└─────────────────────────────────────────────────────────┘
```

## Prerequisites

- AWS credentials configured (`aws configure` or environment variables)
- `formae` binary with **both** plugins installed:
  - `formae-plugin-aws` — for EKS cluster provisioning
  - `formae-plugin-k8s` — for K8S resource management
- `aws` CLI (for kubeconfig generation between stages)
- `kubectl` (for verification)

## Files

```
eks-full-stack/
├── PklProject                    # Imports both AWS and K8S schemas
├── PklProject.deps.json
├── stage1-eks.pkl                # Stage 1: AWS infrastructure
├── stage2-webapp.pkl             # Stage 2: K8S webapp on EKS
├── infrastructure/
│   ├── vars.pkl                  # Shared config + formae props
│   ├── vpc.pkl                   # VPC with K8S cluster tags
│   ├── network.pkl               # IGW, subnets, routes
│   ├── security_groups.pkl       # Cluster + node SGs
│   ├── iam.pkl                   # Cluster + node IAM roles
│   └── eks.pkl                   # EKS cluster (AutoMode)
└── README.md
```

## Stage 1: Provision EKS Cluster

Creates 15 AWS resources:

| Resource | Type | Notes |
|----------|------|-------|
| VPC | `AWS::EC2::VPC` | 10.1.0.0/16, DNS enabled |
| Internet Gateway | `AWS::EC2::InternetGateway` | |
| IGW Attachment | `AWS::EC2::VPCGatewayAttachment` | |
| 2 Public Subnets | `AWS::EC2::Subnet` | us-west-2a/2b, ELB-tagged |
| Route Table | `AWS::EC2::RouteTable` | |
| Public Route | `AWS::EC2::Route` | 0.0.0.0/0 → IGW |
| 2 Subnet Associations | `AWS::EC2::SubnetRouteTableAssociation` | |
| Cluster Security Group | `AWS::EC2::SecurityGroup` | Port 443 ingress |
| Node Security Group | `AWS::EC2::SecurityGroup` | All ports from cluster SG |
| 2 SG Ingress Rules | `AWS::EC2::SecurityGroupIngress` | |
| Cluster IAM Role | `AWS::IAM::Role` | 5 EKS managed policies |
| Node IAM Role | `AWS::IAM::Role` | Worker + ECR policies |
| EKS Cluster | `AWS::EKS::Cluster` | AutoMode, Karpenter, EBS, ELB |

```bash
# Deploy with defaults (us-west-2, eks-fullstack)
formae apply examples/eks-full-stack/stage1-eks.pkl

# Or override region/name
formae apply examples/eks-full-stack/stage1-eks.pkl \
  --prop name=my-cluster --prop region=us-east-1
```

This takes approximately 10-15 minutes. The EKS cluster uses AutoMode — no node groups to manage, Karpenter handles scaling automatically.

## Bridge: Kubeconfig

After Stage 1 completes, generate a kubeconfig for the new cluster:

```bash
aws eks update-kubeconfig \
  --name eks-fullstack \
  --region us-west-2 \
  --alias eks-fullstack

# Verify
kubectl --context eks-fullstack get nodes
```

**Why is this manual?** Formae doesn't support cross-forma output passing yet. The EKS cluster endpoint and CA certificate are outputs from Stage 1 that Stage 2 needs, but there's no mechanism to pipe them. See [Gap Analysis](#gap-analysis) below.

## Stage 2: Deploy Webapp

Before deploying, update the EKS context in `stage2-webapp.pkl`:

```pkl
// Update this line with your actual cluster ARN or kubeconfig alias
local eksContext = "eks-fullstack"   // or the full ARN
```

Then deploy:

```bash
formae apply examples/eks-full-stack/stage2-webapp.pkl
```

This creates 10 K8S resources in the `bookstore` namespace:

| Resource | Type |
|----------|------|
| Namespace | `K8S::Core::Namespace` |
| 2 ConfigMaps | `K8S::Core::ConfigMap` |
| Secret | `K8S::Core::Secret` |
| ServiceAccount | `K8S::Core::ServiceAccount` |
| 2 Deployments | `K8S::Apps::Deployment` |
| 2 Services | `K8S::Core::Service` |
| Ingress | `K8S::Networking::Ingress` |

### Access via Port-Forward

```bash
kubectl --context eks-fullstack -n bookstore \
  port-forward svc/bookstore-frontend 8080:80

open http://localhost:8080
```

## Teardown

Destroy in reverse order:

```bash
# Remove K8S resources
formae destroy examples/eks-full-stack/stage2-webapp.pkl

# Remove AWS infrastructure (VPC, EKS, IAM, etc.)
formae destroy examples/eks-full-stack/stage1-eks.pkl
```

---

## Gap Analysis

This example demonstrates what works today and exposes the gaps in cross-provider orchestration.

### What Works

- **Single-target formas**: Both stage1 (AWS) and stage2 (K8S) work independently
- **Resolvable references within a target**: Subnet IDs, IAM ARNs, SG IDs all resolve correctly within the AWS forma
- **K8S namespace references**: `appNs.res.name` wires namespace into child resources
- **Local eval with remote target**: The local `formae` CLI evaluates PKL and sends operations to the EKS cluster via kubeconfig

### Gaps

| Gap | Severity | Description |
|-----|----------|-------------|
| **No cross-forma outputs** | Critical | Stage 2 can't read Stage 1's EKS endpoint. Manual `aws eks update-kubeconfig` bridges the gap. |
| **No multi-target forma** | Critical | Can't combine AWS + K8S resources in a single forma. Each forma has exactly one Target. |
| **Manual context wiring** | Moderate | The `eksContext` value in stage2 must be manually updated to match the cluster name/ARN from stage1. |
| **No pipeline primitive** | Moderate | No built-in way to express "run stage1, then stage2" as a single operation. |

### What Would Make This Seamless

A hypothetical pipeline:

```pkl
// pipeline.pkl (does not exist today)
amends "@formae/pipeline.pkl"

stages {
  new {
    label = "infra"
    forma = import("./stage1-eks.pkl")
  }
  new {
    label = "app"
    forma = import("./stage2-webapp.pkl")
    depends_on { "infra" }
    // Map outputs from infra to app's target config
    bindings {
      ["eksContext"] = "infra.outputs.eks-cluster.Endpoint"
    }
  }
}
```

Until then, the two-stage approach with manual kubeconfig bridging is the recommended pattern.
