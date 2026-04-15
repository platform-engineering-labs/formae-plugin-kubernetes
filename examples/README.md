# Examples

End-to-end examples deploying real workloads to Kubernetes using Formae and
the K8S plugin. Examples are organized by cloud provider, with shared
reusable app modules in `apps/`.

```
examples/
├── apps/                      # Shared reusable workload modules
│   └── bookstore.pkl          # Full-stack webapp (parameterized by target)
├── eks/                       # AWS EKS (cross-cloud with resolvables)
│   ├── eks.pkl                # Provisions EKS + deploys bookstore
│   └── infrastructure/        # VPC, subnets, IAM, EKS cluster
├── vanilla/                   # Any kubeconfig-accessible cluster
│   └── vanilla.pkl            # Deploys bookstore via KubeconfigAuth
├── charts/                    # Native PKL charts (no Helm)
└── helm/                      # Helm bridge examples
```

## Shared Apps (`apps/`)

Reusable workload modules parameterized by K8S target. Each cloud provider
example imports from `@apps/` — write the app once, deploy to any cluster.

| App | Description |
|-----|-------------|
| `bookstore.pkl` | Frontend (nginx) + backend (Node.js API) with ConfigMaps, Secrets, ServiceAccount, Deployments, Services |

Future:
- `nginx-ingress.pkl` — ingress controller
- `lgtm.pkl` — observability stack (Loki, Grafana, Tempo, Mimir)

## Vanilla K8S

Deploys the bookstore to any cluster accessible via kubeconfig. No cloud
provider plugin required.

```bash
formae apply examples/vanilla/vanilla.pkl
kubectl -n bookstore port-forward svc/bookstore-frontend 8080:80
open http://localhost:8080
formae destroy examples/vanilla/vanilla.pkl
```

## AWS EKS

Provisions a full EKS AutoMode cluster on AWS and deploys the bookstore
using provider-agnostic auth with STS token refresh. Single forma —
no manual kubeconfig step.

Prerequisites:
- AWS credentials configured
- `formae-plugin-aws` installed
- `formae-plugin-k8s` installed

```bash
formae apply --mode reconcile --yes --watch examples/eks/eks.pkl
```

The frontend service gets an AWS LoadBalancer — access it directly from
your browser via the ELB hostname.

```bash
formae destroy --yes examples/eks/eks.pkl
```

## Adding a New Cloud Provider Example

1. Create `examples/<provider>/`
2. Add a `PklProject` with `@formae`, `@k8s`, `@<cloud>`, and `@apps` deps
3. Write `<provider>.pkl` that:
   - Provisions cloud infrastructure
   - Creates a K8S target with the appropriate auth type (GKEAuth, AKSAuth, etc.)
   - Imports `@apps/bookstore.pkl` and deploys with `bookstore.allResources(target)`
4. The bookstore module handles all K8S resources — you only write the infra + auth

## Charts and Helm

See the [charts/](charts/) and [helm/](helm/) directories for deploying
workloads via native PKL charts or Helm bridge. These predate the shared
apps pattern and will be migrated over time.
