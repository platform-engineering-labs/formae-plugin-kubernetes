# Kubernetes Plugin for formae

[![CI](https://github.com/platform-engineering-labs/formae-plugin-kubernetes/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-kubernetes/actions/workflows/ci.yml)
[![Nightly](https://github.com/platform-engineering-labs/formae-plugin-kubernetes/actions/workflows/nightly.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-kubernetes/actions/workflows/nightly.yml)

Kubernetes resource plugin for
[formae](https://github.com/platform-engineering-labs/formae). This plugin
enables formae to manage Kubernetes resources via
[Server-Side Apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/),
with strongly-typed Pkl schemas pinned to your cluster's exact K8s minor
(v1.21 → v1.36, 16 minors).

## Supported Kubernetes versions

Two version ranges are in play, and they are not the same:

| Range | Minors | What it covers |
|-------|--------|----------------|
| **Schema trees** | `1.21` → `1.36` (16 minors) | Typed Pkl authoring and `pkl eval`-time field validation. Select a minor with `kubernetesVersion` on the Target; omitted ⇒ most recent supported minor (`1.36`). |
| **Runtime support window** | `1.31` → `1.36` | Minors the plugin will drive against a live cluster (`MinSupportedK8sVersion` / `MaxSupportedK8sVersion` in `pkg/config/version.go`). Applying against a cluster outside this window returns a clear preflight error. |

So schema trees exist for `1.21` → `1.30`, but those minors are below the
runtime floor: you can author and `pkl eval` against them, yet the plugin
refuses to drive a live cluster older than `1.31`. `client-go` is pinned in
lockstep with the highest supported minor.

## Supported Resources

This plugin supports **38 Kubernetes resource types** across 15 API groups,
plus a generic catch-all for arbitrary custom resources.

| API Group | Resource Type | Description |
|-----------|---------------|-------------|
| Core | `K8S::Core::Namespace` | Virtual cluster partition that scopes names and resources. |
| Core | `K8S::Core::Pod` | Smallest deployable unit — one or more co-located containers. |
| Core | `K8S::Core::Service` | Stable network endpoint and load balancing for a set of pods. |
| Core | `K8S::Core::ConfigMap` | Non-confidential key/value configuration data. |
| Core | `K8S::Core::Secret` | Sensitive key/value data (credentials, tokens, TLS keys). |
| Core | `K8S::Core::ServiceAccount` | Identity for processes running in pods. |
| Core | `K8S::Core::PersistentVolume` | Cluster-level piece of provisioned storage. |
| Core | `K8S::Core::PersistentVolumeClaim` | A workload's request to bind a PersistentVolume. |
| Core | `K8S::Core::Endpoints` | Network addresses backing a Service. |
| Core | `K8S::Core::LimitRange` | Default/min/max resource constraints within a namespace. |
| Core | `K8S::Core::ResourceQuota` | Aggregate resource-usage limits per namespace. |
| Apps | `K8S::Apps::Deployment` | Declarative rollout and scaling of stateless pods. |
| Apps | `K8S::Apps::StatefulSet` | Ordered, stable-identity pods for stateful workloads. |
| Apps | `K8S::Apps::DaemonSet` | Runs one pod per (matching) node. |
| Apps | `K8S::Apps::ReplicaSet` | Maintains a stable set of replica pods. |
| Batch | `K8S::Batch::Job` | Run-to-completion workload. |
| Batch | `K8S::Batch::CronJob` | Schedule-driven Jobs. |
| Networking | `K8S::Networking::Ingress` | HTTP/HTTPS routing rules to Services. |
| Networking | `K8S::Networking::IngressClass` | Selects the ingress controller for an Ingress. |
| Networking | `K8S::Networking::NetworkPolicy` | Pod-level ingress/egress traffic rules. |
| RBAC | `K8S::Rbac::ClusterRole` | Cluster-wide permission set. |
| RBAC | `K8S::Rbac::ClusterRoleBinding` | Binds a ClusterRole to subjects cluster-wide. |
| RBAC | `K8S::Rbac::Role` | Namespace-scoped permission set. |
| RBAC | `K8S::Rbac::RoleBinding` | Binds a Role/ClusterRole within a namespace. |
| Storage | `K8S::Storage::StorageClass` | Dynamic volume-provisioning profile. |
| Storage | `K8S::Storage::CSIDriver` | Registration of a CSI storage driver. |
| Admission Registration | `K8S::Admissionregistration::MutatingWebhookConfiguration` | External mutating admission webhooks. |
| Admission Registration | `K8S::Admissionregistration::ValidatingWebhookConfiguration` | External validating admission webhooks. |
| Admission Registration | `K8S::Admissionregistration::MutatingAdmissionPolicy` | In-tree, CEL-based mutation (GA in 1.36, [KEP-3962](https://kep.k8s.io/3962)); `v1.36+` schema trees only. |
| API Extensions | `K8S::Apiextensions::CustomResourceDefinition` | Defines a new custom resource kind; blocks until `Established`. |
| Custom | `K8S::Custom::Resource` | Generic catch-all for any CRD instance (free-form `spec`, no per-CRD code). |
| Autoscaling | `K8S::Autoscaling::HorizontalPodAutoscaler` | Scales workload replicas on observed metrics. |
| Policy | `K8S::Policy::PodDisruptionBudget` | Minimum availability during voluntary disruptions. |
| Scheduling | `K8S::Scheduling::PriorityClass` | Pod scheduling priority. |
| Coordination | `K8S::Coordination::Lease` | Lightweight lock for leader election / node heartbeats. |
| Flow Control | `K8S::Flowcontrol::FlowSchema` | Classifies API requests into priority levels (APF). |
| Flow Control | `K8S::Flowcontrol::PriorityLevelConfiguration` | Concurrency limits per priority level (APF). |
| Node | `K8S::Node::RuntimeClass` | Selects the container runtime configuration for pods. |

`MutatingAdmissionPolicy` reached GA in K8s 1.36 (KEP-3962) and is only
present in the `v1.36+` schema trees; it cannot be referenced when
`kubernetesVersion` is set to an earlier minor.

**Custom resources.** `K8S::Custom::Resource` manages any CRD instance
(cert-manager `Certificate`, Argo `Application`, and so on) through a generic
catch-all — no per-CRD Go code or generated schema. A CRD and instances of the
kind it defines can live in one forma and deploy in a single `formae apply`.
Both custom types are `discoverable = false`, so they are managed via explicit
declaration only, not surfaced by discovery.

See [`schema/pkl/`](schema/pkl/) for the complete list of supported resource
types.

## Configuration

### Target Configuration

Configure a Kubernetes target in your Forma file:

```pkl
import "@formae/formae.pkl"
import "@k8s/k8s.pkl" as k8s

target: formae.Target = new formae.Target {
  label = "k8s-target"
  namespace = "K8S"
  config = new k8s.Config {
    kubernetesVersion = "1.31"          // K8s minor — selects the schema subtree
    auth = new k8s.KubeconfigAuth {}    // see Credentials below
  }
}
```

`Config` fields:

| Field | Type | Purpose |
|---|---|---|
| `kubernetesVersion` | `String` | K8s minor (e.g. `"1.31"`). Selects the schema subtree the plugin validates against. Supported: `"1.21"`, `"1.22"`, `"1.23"`, `"1.24"`, `"1.25"`, `"1.26"`, `"1.27"`, `"1.28"`, `"1.29"`, `"1.30"`, `"1.31"`, `"1.32"`, `"1.33"`, `"1.34"`, `"1.35"`, `"1.36"`. Omitted ⇒ assumes the most recent supported minor (currently `1.36`). |
| `auth` | `Auth` | One of `KubeconfigAuth`, `EKSAuth`, `GKEAuth`, `AKSAuth`, `OVHAuth`, `OCIAuth`, `InClusterAuth`. |

Every namespaced resource MUST set `metadata.namespace`. Reference a
`K8S::Core::Namespace` declared in the same Forma to keep it single-sourced:

```pkl
import "@k8s/k8s.pkl" as k8s
import "@k8s/v1.31/core/Namespace.pkl" as ns
import "@k8s/v1.31/core/ConfigMap.pkl" as cm

local appNs = new ns.Namespace {
  label = "my-app-ns"
  metadata = new ns.NamespaceMetadata { name = "my-app" }
}

local appConfig = new cm.ConfigMap {
  label = "my-app-config"
  metadata = new k8s.NamespacedObjectMeta {
    name = "my-app-config"
    namespace = appNs.res.name   // resolvable ref into the Namespace above
  }
  data { "log.level" = "info" }
}
```

### Credentials

The plugin supports six authentication strategies. Configure via the `auth`
field on `k8s.Config`:

**Kubeconfig** (local development, any pre-configured cluster):

```pkl
auth = new k8s.KubeconfigAuth {
  context = "kind-formae-test"        // optional — defaults to current-context
  kubeconfig = "/path/to/kubeconfig"  // optional — defaults to $KUBECONFIG / ~/.kube/config
}
```

**Managed clusters** (`EKSAuth`, `GKEAuth`, `AKSAuth`, `OVHAuth`, `OCIAuth`) —
each takes the cluster endpoint, CA, and provider-specific identifiers, with
auth tokens minted from your existing cloud session. See
[`schema/pkl-main/target.pkl`](schema/pkl-main/target.pkl) for the field shapes.

**In-cluster** (when formae itself runs as a Pod):

```pkl
auth = new k8s.InClusterAuth {}
```

The Pod's ServiceAccount token at
`/var/run/secrets/kubernetes.io/serviceaccount/` is used automatically.

## Helm charts

The K8s package ships Helm-chart wrappers under
[`schema/pkl/helm/`](schema/pkl/helm/) that render Helm
charts at Pkl-eval time and map the output to typed K8s resources. Import via
`@k8s/helm/v<X.Y>/HelmChart.pkl`; the wrapper version must match the
`kubernetesVersion` on the Target. Requires `pkl-reader-helm` on `PATH`.

See [`schema/pkl-helm/README.md`](schema/pkl-helm/README.md)
for the wrapper layout and codegen details.

## Examples

The [examples/](examples/) directory has runnable forma files covering two
patterns.

### Cross-cloud workloads

Each workload is a directory of per-cloud entry files —
`examples/<workload>/<cloud>.pkl`, where `<cloud>` is one of `aws`, `azure`,
`gcp`, `oci`, or `local` (a kubeconfig-accessible cluster). Pick a cloud by
choosing the matching file.

| Example | Description |
|---------|-------------|
| [bookstore](examples/bookstore/) | Frontend + backend webapp on a managed cluster |
| [crossplane](examples/crossplane/) | Crossplane control plane on a managed cluster |
| [lgtm-observability](examples/lgtm-observability/) | Grafana + Loki + Tempo + Mimir + OTel + MinIO observability stack |

```bash
# Resolve Pkl deps once per fresh clone
pkl project resolve examples/

# Pick a cloud by choosing the matching file
formae apply --mode reconcile --watch examples/bookstore/local.pkl
formae apply --mode reconcile --watch examples/bookstore/aws.pkl
formae apply --mode reconcile --watch examples/lgtm-observability/azure.pkl
formae apply --mode reconcile --watch examples/crossplane/gcp.pkl
```

Each workload example has a per-directory README with prerequisites, smoke
test commands, and per-provider tear-down steps.

### Helm charts

The [examples/helm/](examples/helm/) directory uses the `@k8s/helm` wrappers
to render Helm charts into typed K8s resources.

| File | What it deploys |
|---|---|
| `nginx-v1.31.pkl` | bitnami/nginx, 2 replicas, ClusterIP service |
| `nginx-v1.34.pkl` | same, pinned to the latest supported minor |
| `memcached-v1.31.pkl` | bitnami/memcached standalone |
| `postgresql-v1.31.pkl` | bitnami/postgresql primary-only |

```bash
pkl eval examples/helm/nginx-v1.31.pkl --project-dir examples/helm/
formae apply examples/helm/nginx-v1.31.pkl --mode reconcile --yes --watch
formae destroy examples/helm/nginx-v1.31.pkl --yes --watch
```

## Targets

Pick a cloud by choosing `examples/<workload>/<cloud>.pkl`:

| Provider | Auth class | Cluster type | Required setup |
|----------|------------|--------------|----------------|
| `aws`    | `EKSAuth`  | EKS AutoMode | `aws configure`; defaults to `us-west-2` |
| `azure`  | `AKSAuth`  | AKS          | `az login`; set `AZURE_SUBSCRIPTION_ID` and `AZURE_PRINCIPAL_ID` (your AAD object id from `az ad signed-in-user show --query id -o tsv`) |
| `gcp`    | `GKEAuth`  | Standard zonal GKE | `gcloud auth application-default login`; set `GCP_PROJECT=<your-project>` (or `GOOGLE_CLOUD_PROJECT`) |
| `oci`    | `OCIAuth`  | OKE          | `oci session authenticate`; set `OCI_COMPARTMENT_ID=<ocid>` |
| `local`  | `KubeconfigAuth` | none — uses your kubectl context | `kubectl config current-context` should resolve to the target cluster |

The cluster-provisioning code lives under [examples/clusters/](examples/clusters/);
each provider's `<cloud>.pkl` is a Pkl module exposing `resources(slug)` and
`target(slug)` functions that the workload examples consume. To add a new
provider, create `examples/clusters/<provider>.pkl` exposing `resources(slug)`
and `target(slug)`, then add a `<provider>.pkl` entry file in each workload
directory.

## License

This plugin is licensed under the [Functional Source License, Version 1.1, ALv2
Future License (FSL-1.1-ALv2)](LICENSE).

Copyright 2026 Platform Engineering Labs Inc.
