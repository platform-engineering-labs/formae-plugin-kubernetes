# formae-helm

Helm bridge for Formae — renders real Helm charts into typed `formae-plugin-k8s` resources.

This package takes any Helm chart (`bitnami/nginx`, `grafana/grafana`, etc.), runs `helm template` under the hood via [`pkl-reader-helm`](https://github.com/apple/pkl-readers/tree/main/helm), and maps every rendered Kubernetes resource into the corresponding typed PKL class from `formae-plugin-k8s`. The result is a standard Formae forma that can be deployed, diffed, and managed like any other Formae configuration.

> **Looking for pure-PKL charts?** NginxChart and LangfuseChart live in [`charts/`](../charts/) — they have zero Helm dependency.

## Prerequisites

- [pkl](https://pkl-lang.org/) 0.30.2+
- [pkl-reader-helm](https://github.com/apple/pkl-readers/tree/main/helm) 0.1.1+ on `PATH`
- [Helm](https://helm.sh/) v3+ with chart repos configured

```bash
# Install pkl-reader-helm (macOS)
brew install pkl-reader-helm

# Add a chart repo
helm repo add bitnami https://charts.bitnami.com/bitnami
helm repo update
```

## Three Ways to Use It

### 1. HelmChart — Low-level resource rendering

`HelmChart.pkl` renders a chart and gives you a `Listing<formae.Resource>` to spread into your own forma. You wire up Stack, Target, and Namespace yourself.

```pkl
amends "@formae/forma.pkl"

import "@formae/formae.pkl"
import "@formae-plugin-k8s/core/Namespace.pkl" as ns
import "@formae-helm/HelmChart.pkl"

local chart = new HelmChart {
  chart = "bitnami/nginx"
  version = "22.4.7"
  releaseName = "my-nginx"
  namespace = "formae-test"
  values = new Dynamic {
    replicaCount = 2
    service { type = "ClusterIP" }
  }
}

forma {
  new formae.Stack { label = "my-app" }
  new formae.Target {
    label = "k8s-local"
    namespace = "K8S"
    config { ["context"] = "orbstack"; ["namespace"] = "formae-test" }
  }
  new ns.Namespace {
    label = "my-nginx-namespace"
    metadata = new ns.NamespaceMetadata { name = "formae-test" }
  }
  for (resource in chart.resources) { resource }
}
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `chart` | `String` | required | Helm chart reference (e.g. `bitnami/nginx`) |
| `version` | `String` | required | Chart version |
| `releaseName` | `String` | required | Helm release name |
| `namespace` | `String` | `"default"` | Target namespace |
| `values` | `Dynamic?` | `null` | Helm values |
| `labelPrefix` | `String` | `releaseName` | Prefix for Formae resource labels |
| `skipUnsupported` | `Boolean` | `true` | Skip unsupported resource types instead of throwing |

### 2. Generator — Complete forma in one object

`Generator.pkl` wraps `HelmChart` and produces everything — Stack, Target, optional Namespace, and all resources — in a single `formaEntries` property.

```pkl
amends "@formae/forma.pkl"

import "@formae-helm/Generator.pkl"

local rel = new Generator.HelmRelease {
  chart = "bitnami/nginx"
  version = "22.4.7"
  releaseName = "my-nginx"
  namespace = "formae-test"
  targetLabel = "k8s-local"
  targetContext = "orbstack"
  createNamespace = true
  values {
    replicaCount = 2
    service { type = "ClusterIP" }
  }
}

forma {
  for (entry in rel.formaEntries) { entry }
}
```

Additional properties beyond HelmChart:

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `stackLabel` | `String` | `releaseName` | Formae stack label |
| `stackDescription` | `String` | auto | Stack description |
| `targetLabel` | `String` | `"k8s"` | Target label |
| `targetContext` | `String?` | `null` | K8S context (null = current-context) |
| `targetKubeconfig` | `String?` | `null` | Path to kubeconfig |
| `createNamespace` | `Boolean` | `false` | Include a Namespace resource |

### 3. StaticGenerator — Generate self-contained PKL

`StaticGenerator.pkl` renders the chart once and outputs PKL source code. The output file is a complete forma that **does not need `pkl-reader-helm` at deploy time** — it's fully self-contained and editable.

```pkl
import "@formae-helm/StaticGenerator.pkl"

local rel = new StaticGenerator.StaticHelmRelease {
  chart = "bitnami/nginx"
  version = "22.4.7"
  releaseName = "my-nginx"
  namespace = "formae-test"
  targetLabel = "k8s-local"
  targetContext = "orbstack"
  createNamespace = true
  values {
    replicaCount = 2
    service { type = "ClusterIP" }
  }
}

output { text = rel.pklSource }
```

```bash
# Generate a static forma (needs pkl-reader-helm)
pkl eval my-chart-static.pkl > my-nginx-forma.pkl

# Deploy it (no pkl-reader-helm needed)
formae apply --mode reconcile my-nginx-forma.pkl
```

This is ideal for CI/CD pipelines or environments where you don't want `pkl-reader-helm` as a runtime dependency.

## CLI Tool: `helm-to-forma`

`bin/helm-to-forma` is a shell wrapper for quick one-off generation from the command line.

### From a spec file

```bash
# PCF output (default)
helm/bin/helm-to-forma examples/helm/nginx-generator.pkl -o nginx.pcf

# Static PKL output
helm/bin/helm-to-forma examples/helm/nginx-static.pkl --format pkl -o nginx.pkl
```

### From CLI arguments

```bash
helm/bin/helm-to-forma \
  --chart bitnami/nginx \
  --version 22.4.7 \
  --release my-nginx \
  --namespace formae-test \
  --target k8s-local \
  --target-context orbstack \
  --create-namespace \
  --set replicaCount=2 \
  --set service.type=ClusterIP \
  -o nginx.pcf
```

| Flag | Description |
|------|-------------|
| `--chart` | Helm chart reference |
| `--version` | Chart version |
| `--release` | Release name |
| `--namespace` | Target namespace (default: `default`) |
| `--stack` | Stack label (default: release name) |
| `--target` | Target label (default: `k8s`) |
| `--target-context` | K8S context |
| `--create-namespace` | Include Namespace resource |
| `--set key=value` | Helm values (repeatable, supports one level of dot nesting) |
| `--format pcf\|pkl` | Output format (default: `pcf`) |
| `-o file` | Output file (default: stdout) |

## Supported Resource Types

The mapper layer converts 30+ Kubernetes resource types:

| API Group | Kinds |
|-----------|-------|
| **core/v1** | Namespace, Pod, ConfigMap, Secret, ServiceAccount, Service, Endpoints, LimitRange, PersistentVolume, PersistentVolumeClaim, ResourceQuota |
| **apps/v1** | Deployment, ReplicaSet, StatefulSet, DaemonSet |
| **batch/v1** | Job, CronJob |
| **networking.k8s.io/v1** | Ingress, IngressClass, NetworkPolicy |
| **rbac.authorization.k8s.io/v1** | Role, RoleBinding, ClusterRole, ClusterRoleBinding |
| **policy/v1** | PodDisruptionBudget |
| **storage.k8s.io/v1** | StorageClass, CSIDriver |
| **scheduling.k8s.io/v1** | PriorityClass |
| **autoscaling/v2** | HorizontalPodAutoscaler |
| **coordination.k8s.io/v1** | Lease |
| **node.k8s.io/v1** | RuntimeClass |
| **admissionregistration.k8s.io/v1** | ValidatingWebhookConfiguration, MutatingWebhookConfiguration |
| **flowcontrol.apiserver.k8s.io/v1** | FlowSchema, PriorityLevelConfiguration |

Unsupported types (CRDs, etc.) are silently skipped by default. Set `skipUnsupported = false` to throw on unknown types.

## How It Works

```
Helm chart (bitnami/nginx)
  │
  ▼  pkl-reader-helm calls `helm template`
Raw K8S manifests (YAML → pkl-k8s types)
  │
  ▼  mappers/dispatch.pkl routes by apiVersion+kind
Per-group mappers (core.pkl, apps.pkl, ...)
  │
  ▼  each mapper converts pkl-k8s → formae-plugin-k8s types
Typed formae.Resource objects
  │
  ▼  spread into forma {} block
Standard Formae forma (deploy, diff, discover)
```

1. **`pkl-reader-helm`** is an external PKL resource reader. When PKL evaluates a `helm.Template`, it shells out to `helm template` and returns the rendered manifests as typed `pkl-k8s` objects.
2. **`mappers/dispatch.pkl`** routes each resource by `(apiVersion, kind)` to a per-API-group mapper module.
3. **Per-group mappers** (e.g. `mappers/apps.pkl`, `mappers/core.pkl`) convert `pkl-k8s` types into the corresponding `formae-plugin-k8s` types, preserving labels, selectors, specs, and all nested structures.
4. **`codegen/render.pkl`** (used by StaticGenerator) takes the mapped resources and emits PKL source code that can be saved as a standalone file.

## Package Structure

```
helm/
├── PklProject              # Package: formae-helm v0.2.0
├── PklProject.deps.json
├── HelmChart.pkl           # Low-level: chart → Listing<Resource>
├── Generator.pkl           # Mid-level: chart → complete forma entries
├── StaticGenerator.pkl     # Code gen: chart → self-contained PKL source
├── bin/
│   └── helm-to-forma       # CLI wrapper script
├── mappers/                # Helm → formae-plugin-k8s type mappers
│   ├── dispatch.pkl        # Router (apiVersion+kind → mapper)
│   ├── common.pkl          # Shared utilities (metadata, labels, pod specs)
│   ├── core.pkl            # core/v1
│   ├── apps.pkl            # apps/v1
│   ├── batch.pkl           # batch/v1
│   ├── networking.pkl      # networking.k8s.io/v1
│   ├── rbac.pkl            # rbac.authorization.k8s.io/v1
│   ├── policy.pkl          # policy/v1
│   ├── storage.pkl         # storage.k8s.io/v1
│   ├── scheduling.pkl      # scheduling.k8s.io/v1
│   ├── autoscaling.pkl     # autoscaling/v2
│   ├── coordination.pkl    # coordination.k8s.io/v1
│   ├── node.pkl            # node.k8s.io/v1
│   ├── admissionregistration.pkl
│   └── flowcontrol.pkl     # flowcontrol.apiserver.k8s.io/v1
└── codegen/                # PKL source code generation
    ├── render.pkl          # Resource → PKL source rendering
    └── writer.pkl          # Low-level code gen utilities
```

## Using as a Dependency

To use `formae-helm` from your own PKL project:

```pkl
// PklProject
amends "pkl:Project"

dependencies {
  ["formae"] {
    uri = "package://hub.platform.engineering/plugins/pkl/schema/pkl/formae/formae@0.82.1"
  }
  ["formae-plugin-k8s"] = import("path/to/formae-plugin-k8s/schema/pkl/PklProject")
  ["formae-helm"] = import("path/to/formae-plugin-k8s/helm/PklProject")
}

evaluatorSettings {
  externalResourceReaders {
    ["reader+helm"] {
      executable = "pkl-reader-helm"
    }
  }
}
```

Then resolve dependencies:

```bash
pkl project resolve
```

The `evaluatorSettings` block is required — without it, PKL won't know how to invoke `pkl-reader-helm` when it encounters Helm chart references.
