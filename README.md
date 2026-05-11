# Kubernetes Plugin for formae

[![CI](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/ci.yml)
[![Nightly](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/nightly.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/nightly.yml)

### Conformance per K8s version

Older versions only run after the next-newer version has passed on `main`.

[![K8s 1.34](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-34.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-34.yml)
[![K8s 1.33](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-33.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-33.yml)
[![K8s 1.32](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-32.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-32.yml)
[![K8s 1.31](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-31.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-31.yml)
[![K8s 1.30](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-30.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-30.yml)
[![K8s 1.29](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-29.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-29.yml)
[![K8s 1.28](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-28.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-28.yml)
[![K8s 1.27](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-27.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-27.yml)
[![K8s 1.26](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-26.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-26.yml)
[![K8s 1.25](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-25.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-25.yml)
[![K8s 1.24](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-24.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-24.yml)
[![K8s 1.23](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-23.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-23.yml)
[![K8s 1.22](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-22.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-22.yml)
[![K8s 1.21](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-21.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-k8s/actions/workflows/conformance-v1-21.yml)

Kubernetes resource plugin for
[formae](https://github.com/platform-engineering-labs/formae). Manages
Kubernetes resources via
[Server-Side Apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/),
with strongly-typed Pkl schemas pinned to your cluster's exact K8s minor.

## Supported K8s versions

The plugin ships one Pkl schema tree per supported K8s minor. Pick the
matching one for your cluster — fields that don't exist in that minor are
absent from the schema, so misconfigurations surface at `pkl eval` time
instead of failing at apply.

Currently shipped: **v1.21 → v1.34** (14 minors). Each one has its own
conformance suite that runs on every push to `main` (badges above).

If you don't set `kubernetesVersion` on the Target, the plugin assumes the
most recent supported minor (currently `1.34`). Set it explicitly for
older clusters.

In a forma file, declare the minor on the Target and import the matching
schema subtree:

```pkl
import "@k8s/k8s.pkl" as k8s
import "@k8s/v1.31/core/Namespace.pkl" as ns

new formae.Target {
  label = "k8s-local"
  config = new k8s.Config {
    kubernetesVersion = "1.31"           // must match the imports above
    auth = new k8s.KubeconfigAuth {}     // or InCluster, etc.
  }
}
```

## Supported resources

35 resource types across 13 API groups:

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

CRDs and arbitrary custom resources are not currently supported. See
[`schema/pkl/`](schema/pkl/) for the full list of typed resource kinds.

### Discovery filters

`formae discover` skips a small set of system-installed resources by default
so a fresh managed cluster (EKS / GKE / AKS / KinD / OrbStack) doesn't drag
control-plane noise into your inventory: system namespaces (`kube-system`,
`kube-public`, `kube-node-lease`), the default `kubernetes` Service, default
ServiceAccounts and their token Secrets, controller-owned Pods / ReplicaSets /
Jobs / Endpoints / Leases, `system:*` ClusterRoles and ClusterRoleBindings,
bootstrap FlowSchemas and PriorityLevelConfigurations, cloud-provider default
StorageClasses (e.g. `gp2`, `standard`, `local-path`), and cloud-provider
admission webhooks (`eks-`, `gke-`, `aks-` prefixed). The full list lives in
`DiscoveryFilters()` in [`k8s.go`](k8s.go) — every entry is commented so an
operator who actually wants to manage one of these can drop the matching
filter and rebuild.

## Target configuration

```pkl
import "@formae/formae.pkl"
import "@k8s/k8s.pkl" as k8s

new formae.Target {
  label = "k8s-target"
  namespace = "K8S"
  config = new k8s.Config {
    kubernetesVersion = "1.31"           // K8s minor — matches @k8s/v1.31/* imports
    auth = new k8s.KubeconfigAuth {}     // see Authentication below
  }
}
```

`Config` fields:

| Field | Type | Purpose |
|---|---|---|
| `kubernetesVersion` | `String` | K8s minor (e.g. `"1.31"`). Selects the schema subtree the plugin uses to validate the resource. **If omitted, the plugin assumes the most recent supported minor** (currently `1.34`). Set it explicitly for older clusters so field-level mismatches surface at `pkl eval`. |
| `auth` | `Auth` | One of `KubeconfigAuth`, `EKSAuth`, `GKEAuth`, `AKSAuth`, `OVHAuth`, `OCIAuth`. |

Every namespaced resource MUST set `metadata.namespace` in its PKL. There is no target-level default — the plugin returns an error if `metadata.namespace` is missing on a namespaced kind. Reference a `K8S::Core::Namespace` resource declared in the same Forma to keep the namespace single-sourced:

```pkl
local appNs = new namespace.Namespace {
  metadata { name = "my-app" }
}

forma {
  appNs
  new deployment.Deployment {
    metadata {
      name = "api"
      namespace = appNs.res.name   // resolvable ref into the namespace above
    }
    spec { ... }
  }
}
```

### Authentication

**Kubeconfig** (default for local development):

```pkl
auth = new k8s.KubeconfigAuth {
  // Optional — defaults to current-context in $KUBECONFIG or ~/.kube/config
  context = "kind-formae-test"
  // Optional override for the kubeconfig path
  kubeconfig = "/path/to/kubeconfig"
}
```

**In-cluster** (when formae itself runs as a pod):

```pkl
auth = new k8s.InClusterAuth {}
```

The pod's ServiceAccount token at
`/var/run/secrets/kubernetes.io/serviceaccount/` is used automatically.

## Helm charts via formae-helm

The companion `formae-helm` Pkl package ([helm/](helm/)) renders Helm charts
at Pkl-eval time and maps the output to typed K8s resources, so you can
manage Helm releases through the same forma → reconcile → drift loop as
hand-written resources.

```pkl
amends "@formae/forma.pkl"

import "@formae/formae.pkl"
import "@k8s/k8s.pkl" as k8s
import "@k8s/v1.31/core/Namespace.pkl" as ns
import "@formae-helm/v1.31/HelmChart.pkl"

local chart = new HelmChart {
  chart = "bitnami/nginx"
  version = "22.4.7"
  releaseName = "my-nginx"
  namespace = "demo"
  values = new Dynamic {
    replicaCount = 2
    service { type = "ClusterIP" }
  }
}

forma {
  new formae.Stack { label = "helm-nginx" }
  new formae.Target {
    label = "k8s-local"
    namespace = "K8S"
    config = new k8s.Config {
      kubernetesVersion = "1.31"
      auth = new k8s.KubeconfigAuth {}
    }
  }
  new ns.Namespace {
    label = "demo-namespace"
    metadata = new ns.NamespaceMetadata { name = "demo" }
  }
  for (resource in chart.resources) {
    resource
  }
}
```

Requires `pkl-reader-helm` on `PATH` (a Pkl reader that shells out to `helm
template`). `chart.resources` is a `Listing<formae.Resource>` typed against
the same `@k8s/v<X.Y>/...` schema you'd use for hand-written resources —
mismatched fields fail at eval, not at apply.

The wrapper version must match the `kubernetesVersion` on the Target —
`@formae-helm/v1.31` ↔ `@k8s/v1.31` ↔ `kubernetesVersion = "1.31"`. See
[helm/README.md](helm/README.md) for layout and codegen details.

## Examples

The [examples/](examples/) directory has runnable forma files. The most
focused subset is [examples/helm/](examples/helm/):

| File | What it deploys |
|---|---|
| `nginx-v1.31.pkl` | bitnami/nginx, 2 replicas, ClusterIP service |
| `nginx-v1.34.pkl` | same, pinned to the latest supported minor |
| `memcached-v1.31.pkl` | bitnami/memcached standalone |
| `postgresql-v1.31.pkl` | bitnami/postgresql primary-only |

```bash
# Evaluate
pkl eval examples/helm/nginx-v1.31.pkl --project-dir examples/helm/

# Apply
formae apply examples/helm/nginx-v1.31.pkl --mode reconcile --yes --watch

# Destroy
formae destroy examples/helm/nginx-v1.31.pkl --yes --watch
```

## License

This plugin is licensed under the [Functional Source License, Version 1.1, ALv2
Future License (FSL-1.1-ALv2)](LICENSE).

Copyright 2026 Platform Engineering Labs Inc.
