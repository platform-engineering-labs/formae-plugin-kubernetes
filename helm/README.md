# formae-helm

Per-K8s-version PKL wrappers around [`pkl-readers/helm`](https://github.com/apple/pkl-pantry/tree/main/packages/helm) that map Helm chart output to Formae K8s plugin resources.

## Usage

In a Forma file, pick the K8s minor your target cluster runs:

```pkl
amends "@formae/forma.pkl"
import "@formae-helm/v1.27/HelmChart.pkl"

local nginx = new HelmChart {
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
  // stack, target...
  for (resource in nginx.resources) {
    resource
  }
}
```

`HelmChart.resources` returns a `Listing<formae.Resource>` typed against the K8s schema for that exact minor. Resources whose Kinds don't exist in your minor (e.g. `flowcontrol.apiserver.k8s.io/v1.FlowSchema` before 1.29) are silently skipped — set `skipUnsupported = false` to throw instead.

Three top-level wrappers ship under each `v<X.Y>/`:

| Module | Purpose |
|---|---|
| `HelmChart.pkl` | Class — render a chart at evaluation time, expose `resources` for spread into a `forma {}` block. |
| `Generator.pkl` | Higher-level helper — bundles stack/target with the chart in one object. |
| `StaticGenerator.pkl` | Renders Helm output to **self-contained PKL source** (no `pkl-reader-helm` needed at deploy time). |

## Layout

```
.
├── PklProject               package = formae-helm; depends on @k8s, @formae, @helm
├── main/                    SOURCE OF TRUTH — hand-edited
│   ├── HelmChart.pkl
│   ├── Generator.pkl
│   ├── StaticGenerator.pkl
│   ├── mappers/             one per K8s API group (apps.pkl, batch.pkl, ...)
│   └── codegen/             plumbing for StaticGenerator (no K8s imports)
├── generated/               WRITTEN BY make generate — gitignored
│   ├── v1.21/ ...           per-K8s-minor copy with imports rewritten
│   └── v1.34/
├── tools/gen-versioned-helm Go codegen (~300 LoC)
└── Makefile                 generate / package / test / clean
```

`generated/` is materialised by `make generate`. Each per-version tree is a string-rewrite of `main/` with `@k8s/<group>/<Kind>.pkl` → `@k8s/v<X.Y>/<group>/<Kind>.pkl`, plus mappers dropped when their K8s types don't exist in the target minor (and `dispatch.pkl` patched accordingly).

## Development

Prereqs: `pkl` (0.30+), `go` (1.23+), `make`.

```bash
# 1. Resolve dependencies (writes PklProject.deps.json).
pkl project resolve .

# 2. Regenerate generated/ from main/.
make generate

# 3. Smoke-check that every generated tree resolves all imports.
make test
```

Editing workflow:

- Hand-edit files under `main/`.
- Run `make generate` to refresh `generated/`.
- Run `make test` to catch broken imports immediately.

The `k8s` Pkl dependency in `PklProject` points at the local `formae-plugin-k8s` checkout for development; release builds resolve it against the published `package://hub.platform.engineering/.../k8s@<min>` URI.

## Releasing

Tag a version, push — CI publishes the package zip to the hub:

```bash
git tag v0.3.0 && git push --tags
```

The published zip ships both `main/` and `generated/` so consumers don't need to run codegen themselves; they just declare a dep on `formae-helm@<ver>` and import `@formae-helm/v<X.Y>/HelmChart.pkl`.

## Why a separate repo

- Helm wrappers don't run inside the K8s plugin binary — they are pure PKL evaluated by user Forma files. Bundling them in the K8s plugin install would couple their release cadence unnecessarily.
- A bug fix in the helm dispatch shouldn't require a new K8s plugin release.
- Same dependency model as `pkl-readers/helm` itself (an external Pkl package the K8s plugin doesn't ship).

## License

FSL-1.1-ALv2 — see [LICENSE](LICENSE).
