# Kubernetes Plugin for formae

[![CI](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/ci.yml)
[![Nightly](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/nightly.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/nightly.yml)

Kubernetes resource plugin for
[formae](https://github.com/platform-engineering-labs/formae). This plugin
enables formae to manage Kubernetes resources using
[Server-Side Apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/).

## Supported Resources

This plugin supports **35 Kubernetes resource types** across 13 API groups:

| API Group | Resources | Examples |
|-----------|-----------|----------|
| Core | 11 | Namespace, Pod, Service, ConfigMap, Secret, PersistentVolume |
| Apps | 4 | Deployment, StatefulSet, DaemonSet, ReplicaSet |
| Batch | 2 | Job, CronJob |
| Networking | 3 | Ingress, IngressClass, NetworkPolicy |
| RBAC | 4 | ClusterRole, ClusterRoleBinding, Role, RoleBinding |
| Storage | 2 | StorageClass, CSIDriver |
| Admission Registration | 2 | MutatingWebhookConfiguration, ValidatingWebhookConfiguration |
| Autoscaling | 1 | HorizontalPodAutoscaler |
| Policy | 1 | PodDisruptionBudget |
| Scheduling | 1 | PriorityClass |
| Coordination | 1 | Lease |
| Flow Control | 2 | FlowSchema, PriorityLevelConfiguration |
| Node | 1 | RuntimeClass |

See [`schema/pkl/`](schema/pkl/) for the complete list of supported resource
types.

## Configuration

### Target Configuration

Configure a Kubernetes target in your forma file:

```pkl
import "@formae/formae.pkl"
import "@k8s/k8s.pkl"

target: formae.Target = new formae.Target {
  label = "k8s-target"
  config = new k8s.Config {
    context = "my-cluster"
    namespace = "my-namespace"
    // Optional: specify kubeconfig path
    // kubeconfig = "/path/to/kubeconfig"
  }
}
```

### Credentials

The plugin uses the standard Kubernetes credential chain. Configure access using
one of:

**Kubeconfig (default):**

```bash
# Uses ~/.kube/config or $KUBECONFIG by default
export KUBECONFIG="/path/to/kubeconfig"
```

**Context Selection:**

```bash
# Use a specific context from kubeconfig
kubectl config use-context my-cluster
```

**In-Cluster:** When running inside a Kubernetes cluster, credentials are
automatically retrieved from the service account token.

## Examples

See the [examples/](examples/) directory for usage examples.

```bash
# Evaluate an example
formae eval examples/webapp.pkl

# Apply resources
formae apply --mode reconcile --watch examples/webapp.pkl
```

## License

This plugin is licensed under the [Functional Source License, Version 1.1, ALv2
Future License (FSL-1.1-ALv2)](LICENSE).

Copyright 2026 Platform Engineering Labs Inc.
