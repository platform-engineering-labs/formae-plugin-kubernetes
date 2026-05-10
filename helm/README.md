# formae-helm

Per-K8s-version PKL wrappers around [`pkl-readers/helm`](https://github.com/apple/pkl-pantry/tree/main/packages/helm) that map Helm chart output to formae K8s plugin resources.

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

Only one top-level wrapper ships under each `v<X.Y>/`:

| Module | Purpose |
|---|---|
| `HelmChart.pkl` | Class — render a chart at evaluation time, expose `resources` for spread into a `forma {}` block. |

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
└── Makefile                 generate / package / test / clean
```

Per-version trees are committed. They're emitted by `make generate` as a string-rewrite of `shared/` with `@k8s/<group>/<Kind>.pkl` → `@k8s/v<X.Y>/<group>/<Kind>.pkl`, plus mappers dropped when their K8s types don't exist in the target minor (and `dispatch.pkl` patched accordingly).

## Development

Prereqs: `pkl` (0.30+), `go` (1.23+), `make`.

```bash
# 1. Resolve dependencies (writes PklProject.deps.json).
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

The `k8s` Pkl dependency in `PklProject` points at the local `formae-plugin-k8s` checkout for development; release builds resolve it against the published `package://hub.platform.engineering/.../k8s@<min>` URI.

## Releasing

Tag a version, push — CI publishes the package zip to the hub:

```bash
git tag v0.4.0 && git push --tags
```

The published zip ships `shared/` plus every `v<X.Y>/` so consumers don't need to run codegen themselves; they just declare a dep on `formae-helm@<ver>` and import `@formae-helm/v<X.Y>/HelmChart.pkl`.

## Why a separate repo

- Helm wrappers don't run inside the K8s plugin binary — they are pure PKL evaluated by user Forma files. Bundling them in the K8s plugin install would couple their release cadence unnecessarily.
- A bug fix in the helm dispatch shouldn't require a new K8s plugin release.
- Same dependency model as `pkl-readers/helm` itself (an external Pkl package the K8s plugin doesn't ship).

## License

FSL-1.1-ALv2 — see [LICENSE](LICENSE).
