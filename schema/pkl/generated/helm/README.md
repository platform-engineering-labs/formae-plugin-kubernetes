# formae-helm

Per-K8s-version PKL wrappers around [`pkl-readers/helm`](https://github.com/apple/pkl-pantry/tree/main/packages/helm) that render Helm charts at Pkl-eval time and map the output to typed K8s plugin resources.

The result: Helm releases manageable through the same forma → reconcile → drift loop as hand-written K8s resources, with field-level type safety provided by the `@k8s/v<X.Y>/...` schemas.

## How it fits together

```
   Forma (.pkl)
       │
       │ imports
       ▼
   HelmChart wrapper  ──►  pkl-reader-helm  ──►  helm template  ──►  rendered YAML
       │                     (binary on PATH)
       │
       │ dispatch.pkl routes each rendered Kind to a typed mapper
       ▼
   Listing<formae.Resource>           one per Kind, typed against @k8s/v<X.Y>/...
       │
       │ for-loop spreads into forma { ... }
       ▼
   formae apply  ──►  K8s plugin  ──►  Server-Side Apply
```

`HelmChart.resources` returns a `Listing<formae.Resource>` typed against the K8s schema for that exact minor. Resources whose Kinds don't exist in your minor (e.g. `flowcontrol.apiserver.k8s.io/v1.FlowSchema` before 1.29) are silently skipped — set `skipUnsupported = false` to throw instead.

## Prerequisites

- `pkl` 0.30+
- `pkl-reader-helm` on `PATH` (an external Pkl reader that shells out to `helm template`)
- `helm` 3+ with the chart repos you reference, e.g.:
  ```bash
  helm repo add bitnami https://charts.bitnami.com/bitnami
  helm repo update
  ```
- The matching `@k8s/helm/v<X.Y>` import — must line up with the `kubernetesVersion` on the Target and the `@k8s/v<X.Y>` schema imports in the rest of the forma.

### Install `pkl-reader-helm`

Pre-built binaries are published on the [`apple/pkl-readers` releases page](https://github.com/apple/pkl-readers/releases) under the `helm@<ver>` tags. Pick the asset for your OS/arch, drop it on your `PATH` as `pkl-reader-helm`, and make it executable.

```bash
# Pick the latest helm@<ver> tag from https://github.com/apple/pkl-readers/releases
VER=0.1.2

# Asset name by platform:
#   macOS (universal): pkl-reader-helm-macos.bin
#   Linux x86_64:      pkl-reader-helm-linux-amd64.bin
#   Linux arm64:       pkl-reader-helm-linux-aarch64.bin
case "$(uname -s)-$(uname -m)" in
  Darwin-*)        ASSET=pkl-reader-helm-macos.bin ;;
  Linux-x86_64)    ASSET=pkl-reader-helm-linux-amd64.bin ;;
  Linux-aarch64|Linux-arm64) ASSET=pkl-reader-helm-linux-aarch64.bin ;;
  *) echo "unsupported platform"; exit 1 ;;
esac

curl -fL \
  "https://github.com/apple/pkl-readers/releases/download/helm@${VER}/${ASSET}" \
  -o /usr/local/bin/pkl-reader-helm
chmod +x /usr/local/bin/pkl-reader-helm

pkl-reader-helm version   # sanity check
```

Use `~/.local/bin` (or any other directory on your `PATH`) if you don't want to write to `/usr/local/bin`.

> Match the binary major version to the `pkl-readers/helm@<ver>` package version pinned in [`PklProject`](PklProject). A different patch version (e.g. binary `0.1.2` against package `0.1.1`) is normally fine — the wire protocol is stable within a minor.

## Usage

```pkl
amends "@formae/forma.pkl"

import "@formae/formae.pkl"
import "@k8s/k8s.pkl" as k8s
import "@k8s/v1.31/core/Namespace.pkl" as ns
import "@k8s/helm/v1.31/HelmChart.pkl"

local chart = new HelmChart {
  chart       = "bitnami/nginx"
  version     = "22.4.7"
  releaseName = "my-nginx"
  namespace   = "demo"
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

Apply / destroy:

```bash
formae apply <forma>.pkl --mode reconcile --yes --watch
formae destroy <forma>.pkl --yes --watch
```

`HelmChart` fields:

| Field | Type | Default | Purpose |
|---|---|---|---|
| `chart` | `String` | _(required)_ | Helm chart reference (`<repo>/<chart>` or OCI URL). |
| `version` | `String` | _(required)_ | Chart version. |
| `releaseName` | `String` | _(required)_ | Helm release name. Used as label prefix. |
| `namespace` | `String` | `"default"` | Target namespace for namespaced resources. |
| `values` | `Dynamic?` | `null` | Helm values overrides. Use `new Dynamic { ... }`. |
| `labelPrefix` | `String` | `releaseName` | Prefix used on Formae resource labels. |
| `skipUnsupported` | `Boolean` | `true` | Skip resource Kinds the K8s minor doesn't ship. Set `false` to throw. |

## Version coupling

`@k8s/helm/v<X.Y>` ↔ `@k8s/v<X.Y>` ↔ `Config.kubernetesVersion = "<X.Y>"`. All three must agree. The wrappers enforce this implicitly: the per-version mapper imports types from `@k8s/v<X.Y>/<group>/<Kind>.pkl`, so a mismatch fails at `pkl eval` rather than at apply time.

To deploy the same chart against multiple K8s minors, write one forma per minor (or parameterise the file via Pkl `properties`). Currently shipped: **v1.21 → v1.34** (14 minors).

## Module surface

Only one module is published per minor:

| Module | Purpose |
|---|---|
| `HelmChart.pkl` | Class — render a chart at evaluation time, expose `resources` for spread into a `forma {}` block. |

`Generator.pkl` and `StaticGenerator.pkl` (a static-PKL emitter) shipped in earlier versions but are dropped — out of scope for the current focus on the typed-`HelmChart` flow.

## Layout

```
.
├── PklProject               package = formae-helm; depends on @k8s, @formae, @helm
├── shared/                  SOURCE OF TRUTH — hand-edited
│   ├── HelmChart.pkl
│   └── mappers/             one per K8s API group (apps.pkl, batch.pkl, ...)
├── v1.21/ ...               GENERATED — per-K8s-minor copy with imports rewritten
├── v1.34/
├── tools/gen-versioned-helm Go codegen (~300 LoC)
└── Makefile                 generate / package / test / verify / clean
```

Per-version trees are committed (so `pkl analyze imports` and `pkl eval` work against a checkout without re-running codegen). They're emitted by `make generate` as a string-rewrite of `shared/`:

- `@k8s/<group>/<Kind>.pkl` → `@k8s/v<X.Y>/<group>/<Kind>.pkl` in every file.
- Mappers whose K8s types don't exist in a given minor are dropped (e.g. `flowcontrol.pkl` is omitted under v1.21..v1.28).
- `dispatch.pkl` is patched to remove the imports + branches that pointed at dropped mappers.

### Why per-version trees instead of one parameterised module

Pkl's typed imports are static. A mapper that constructs `new dep.Deployment { ... }` MUST literally `import "@k8s/v<X.Y>/apps/Deployment.pkl" as dep` — you can't parameterise the path or pass types as values. The 14 trees are the price of strong typing on chart output. The codegen does the minimum string-rewrite.

## Development

Prereqs: `pkl` (0.30+), `go` (1.23+), `make`.

```bash
# 1. Resolve Pkl dependencies (writes PklProject.deps.json).
pkl project resolve .

# 2. Regenerate v*/ from shared/.
make generate

# 3. Smoke-check that every generated tree resolves all imports.
make test
```

Editing workflow:

- Hand-edit files under `shared/`.
- Run `make generate` to refresh `v*/`.
- Run `make test` to catch broken imports immediately.
- Commit the diff under `v*/` together with the `shared/` change — `make verify` fails CI on stale trees.

The `k8s` Pkl dependency in `PklProject` points at the local `formae-plugin-kubernetes` checkout for development. Release builds resolve it against the published `package://hub.platform.engineering/.../k8s@<min>` URI.

## Releasing

Tag a version, push — CI publishes the package zip to the hub:

```bash
git tag v0.4.0 && git push --tags
```

The published zip ships `shared/` plus every `v<X.Y>/` so consumers don't need to run codegen themselves. They declare a dep on `formae-helm@<ver>` and import `@k8s/helm/v<X.Y>/HelmChart.pkl` directly.

## Why a separate Pkl package

- The wrappers don't run inside the K8s plugin binary — they're pure PKL evaluated at forma-eval time. Shipping them with the plugin install would couple release cadences unnecessarily.
- A bug fix in helm dispatch shouldn't force a new K8s plugin release.
- Same dependency model as `pkl-readers/helm` itself — an external Pkl package the K8s plugin doesn't ship.

## License

FSL-1.1-ALv2 — see [LICENSE](LICENSE).
