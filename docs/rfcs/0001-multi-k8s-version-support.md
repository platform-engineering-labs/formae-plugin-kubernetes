# RFC-0001: Multi-K8s-version Support for the K8S Formae Plugin

- **Status:** Draft (PR #45, branch `feat/k8s-version-annotations`)
- **Author:** Nico (drafted with Claude during the build-out)
- **Scope:** `formae-plugin-k8s`
- **Supersedes:** the previous single-version pinning (schema targeted whatever
  `k8s.io/api` version was in `go.mod`)

## 1. Motivation

The K8S plugin had one PKL schema and one set of conformance fixtures. That
worked when the only consumer was a single K8s release, but every Forma user
now runs *some* K8s minor between 1.21 and 1.34, and each minor:

- Adds new fields (e.g. `Service.spec.trafficDistribution` in 1.30,
  `Pod.spec.schedulingGates` in 1.26, `CronJob.spec.timeZone` in 1.24).
- Sometimes deprecates or removes fields (e.g. `Service.spec.externalIPs`
  scheduled removal in 1.36).
- Promotes feature gates (alpha → beta → GA), so a fixture that worked on
  1.34 may produce a hard API error on 1.30.

A single static schema produced two failure modes:

1. **Forward-incompatibility.** Schema lacks a new field → Forma extract
   silently drops it on round-trip, drift detection fires falsely.
2. **Backward-incompatibility.** Schema has a 1.30+ field → users on 1.27
   submit Pkl that the API server rejects with an opaque message.

Both classes of bug were caught only by manual testing against specific
clusters. We want a plugin that can be **pinned at install time** to a
specific K8s minor and stays correct against the user's actual cluster
version, plus a CI that exercises every supported minor.

## 2. Design overview

```
schema/pkl/main/                 ← canonical, hand-edited; carries
  k8s.pkl                          @K8sVersion annotations
  core/Pod.pkl                   ↓ (gen-versioned-reflect)
  core/Service.pkl
  ...                            schema/pkl/generated/v1.21/
                                 schema/pkl/generated/v1.22/
                                 ...
                                 schema/pkl/generated/v1.34/

testdata/main/                   ← canonical fixtures
  shared/                          (version-agnostic resource fixtures)
  features/<feature>/              (version-gated fixtures with meta.pkl)
                                 ↓ (gen-versioned-testdata)
                                 testdata/generated/v1.21/{shared,features}/
                                 testdata/generated/v1.22/{shared,features}/
                                 ...

pkg/k8sversion/                  ← Go-side runtime registry mirroring the
                                   PKL annotations (preflight checks)

.github/workflows/
  conformance-version.yml        ← reusable matrix workflow (one K8s minor)
  conformance-pr.yml             ← serial chain of versions for PR CI
  conformance-v1-XX.yml          ← per-version push/workflow_run entry
                                   points (one per minor → status badge)
```

The flow:

- **At edit time** authors touch only `schema/pkl/main/` and
  `testdata/main/`. They mark new fields with `@K8sVersion {...}`.
- **At gen time** (`make generate-versioned-schemas`) the generators walk
  the master tree and emit one fully-resolved copy per K8s minor. The
  generated trees are committed so installs and CI don't depend on the
  generator running cleanly in every environment.
- **At install time** `make install INSTALL_K8S_VERSION=1.32` selects a
  generated tree and copies it into `~/.pel/formae/plugins/k8s/v0.1.1/schema/pkl/`.
- **At runtime** the plugin reads the cluster's K8s minor (from
  `kubectl version`) and uses `pkg/k8sversion` to preflight check user
  payloads against the fields they reference.
- **In CI** every supported minor runs the full shared-fixture
  conformance matrix. PRs run the chain serially newest-first; main runs
  per-version workflows chained via `workflow_run` so each minor has a
  dedicated status badge.

## 3. Authoring the schema: `@K8sVersion`

Defined in `schema/pkl/main/k8s.pkl`:

```pkl
/// All version values are MAJOR.MINOR (e.g., "1.30"), no patch.
class K8sVersion extends Annotation {
  introducedIn: String?
  deprecatedIn: String?
  removedIn: String?
  reference: String?
}
```

Annotate any field, class, or module that didn't exist throughout the
support window. Example:

```pkl
@FieldHint {}
@K8sVersion { introducedIn = "1.26"; reference = "https://kep.k8s.io/3521" }
schedulingGates: Listing<PodSchedulingGate>?
```

Rules of thumb:

- Record the **GA version** (not alpha/beta) for `introducedIn`. We don't
  test against feature-gate-enabled clusters in CI.
- Always include a `reference` (KEP URL preferred). Future readers need
  it; the registry generator surfaces it in error messages.
- Annotate at the leaf field, not the parent class, unless an entire
  class only exists in newer versions (e.g. `gateway.networking.k8s.io`
  resources will be class-level).

## 4. Schema generator: `tools/gen-versioned-reflect`

`make generate-versioned-pkl-schemas` runs:

```
go run ./tools/gen-versioned-reflect \
    --target=1.21 ... --target=1.34 \
    --in=schema/pkl/main \
    --out-dir=schema/pkl/generated
pkl project resolve schema/pkl/generated/v<X.Y>   (each version)
```

Pipeline:

1. **Discovery.** `discover.pkl` uses `pkl:reflect` to walk every module
   under `--in` and emit a JSON manifest of every annotated declaration:
   `(module, member, gate)`.
2. **Filtering.** For each `--target`, the Go tool drops any declaration
   whose gate excludes that target:
   - `introducedIn = "X"` with `X > target` → field/class dropped
   - `removedIn = "X"` with `X ≤ target` → field/class dropped
3. **Emission.** The surviving Pkl is rewritten with the `@K8sVersion`
   annotation lines stripped (so the output looks like a hand-written
   schema for that version). `PklProject` is rewritten so the package
   name becomes `k8s-v<X-Y>` (hyphen-only — Pkl identifiers reject `.`).
4. **Resolution.** `pkl project resolve` regenerates `PklProject.deps.json`.

The generated trees are **committed to the repo**. CI's
`make verify-generated-schemas` regenerates and `git diff --exit-code`s
to ensure they stay in sync with `schema/pkl/main/`.

The list of generated minors lives in
`tools/gen-versioned-reflect/versions.pkl` (currently 1.21 – 1.34).

## 5. Testdata generator: `tools/gen-versioned-testdata`

Same pattern, different input. `testdata/main/` has two children:

- `shared/` — resource fixtures (one per resource type) that are
  version-agnostic. Copied verbatim into every generated tree.
- `features/<feature-name>/` — fixtures that exercise gated fields. Each
  feature dir contains a `meta.pkl`:

  ```pkl
  minK8sVersion = "1.27"
  description = "CronJob with IANA time zone (KEP-3140). GA in 1.27."
  ```

  At generation time the tool runs `pkl eval --format json meta.pkl` and
  copies the dir only into target trees whose minor is `≥ minK8sVersion`
  (and `< maxK8sVersion` if set). `meta.pkl` itself is intentionally
  **not** copied into the generated tree; the conformance harness
  discovers fixtures by scanning `*.pkl`, and a meta module would
  collide.

Charts (`testdata/main/charts/*-chart.pkl`) are deliberately kept out of
both `shared/` and `features/` so the schema/testdata generator skips
them entirely.

## 6. Runtime registry: `pkg/k8sversion`

The plugin reads the cluster's K8s minor (via `kubectl-equivalent` API
calls in `pkg/transport`) and uses `pkg/k8sversion` to preflight a Forma
payload before submitting a Server-Side Apply.

```go
type Gate struct { IntroducedIn, DeprecatedIn, RemovedIn, Reference string }

func CheckField(resourceType, fieldPath, clusterVersion string) error
func CheckPaths(resourceType string, fieldPaths []string, clusterVersion string) error
func PathsForResource(resourceType string) []string
```

The registry is **hand-maintained** today and lives at
`pkg/k8sversion/registry.go`. Every entry mirrors a `@K8sVersion` PKL
annotation. When you add or change a PKL annotation you MUST update the
Go registry in the same commit. A future drift-detector test will make
that automatic; for now, code review is the gate.

The runtime check returns a clear client-side error when a user submits
a payload that uses a field not yet GA on their cluster:

```
field "spec.trafficDistribution" on K8S::Core::Service requires
Kubernetes 1.30, cluster reports 1.29 (see https://kep.k8s.io/4444)
```

## 7. Install pinning

`make install INSTALL_K8S_VERSION=<minor>` selects which generated tree
ships with the plugin. The installer:

1. Verifies `schema/pkl/generated/v<minor>/` exists, regenerating if not.
2. Copies the generated schema to
   `~/.pel/formae/plugins/k8s/v0.1.1/schema/pkl/`.
3. Rewrites the installed `PklProject`'s `name` from `k8s-v<X-Y>` back to
   `k8s` so Forma's extract resolves type modules under the canonical
   name.
4. Re-runs `pkl project resolve` so `PklProject.deps.json` reflects the
   rewrite.
5. Deliberately does NOT install the per-version `generated/` tree
   alongside — Forma's extract walks the entire schema dir to resolve
   type modules, and a second copy of `k8s.pkl` under
   `schema/pkl/generated/v<X.Y>/k8s.pkl` produces an import alias like
   `v1_34_k8s` which is fine syntactically but confuses extract's
   resource-type-name → module-path resolution.

`INSTALL_K8S_VERSION` defaults to the newest entry in
`tools/gen-versioned-reflect/versions.pkl`.

## 8. CI: how it tests every version

### 8.1 Reusable workflow

`.github/workflows/conformance-version.yml` is a `workflow_call`-only
workflow that takes `(k8s_version, kind_version, kind_image)` inputs and
runs the per-fixture parallel matrix for one K8s minor:

- `discover` — lists fixtures from `testdata/generated/v<X.Y>/shared/`
  and emits a JSON matrix.
- `conformance` — `max-parallel: 10`, one matrix entry per fixture, KinD
  pinned to the matching node image, `INSTALL_K8S_VERSION` set, retries
  via `nick-fields/retry@v3` (`max_attempts: 2 × timeout_minutes: 7 ≤
  20min job timeout`) to absorb transient formae plugin-spawn flakes.

Charts and the per-feature matrix are intentionally excluded from CI
right now (charts are kept under `testdata/main/charts/` for a separate
use case; features were excluded after one specific fixture
[`service-traffic-distribution`] turned up a real plugin-update bug we
deferred).

### 8.2 PR triggers — serial chain

`.github/workflows/conformance-pr.yml`:

```yaml
on: { pull_request: { branches: [main] } }
jobs:
  v1_34: { uses: ./.github/workflows/conformance-version.yml, with: ... }
  v1_33: { needs: [v1_34], uses: ..., with: { k8s_version: "1.33", ... } }
  v1_32: { needs: [v1_33], ... }
  v1_31: { needs: [v1_32], ... }
  ...
```

Newest first. A regression in 1.34 short-circuits the chain instead of
burning runner capacity on older versions. Older versions are added
incrementally as each one stabilises.

### 8.3 Main triggers — per-version badges

One workflow file per minor:

- `conformance-v1-34.yml` runs on `push: branches: [main]` and on
  manual dispatch.
- `conformance-v1-33.yml` runs on `workflow_run` of *Conformance K8s
  1.34* completing successfully.
- `conformance-v1-32.yml` chains off 1.33's success, etc.

Each file has a stable name → stable status-badge URL:

```markdown
[![K8s 1.34](.../actions/workflows/conformance-v1-34.yml/badge.svg?branch=main)]
[![K8s 1.33](.../actions/workflows/conformance-v1-33.yml/badge.svg?branch=main)]
...
```

PRs and main thus exercise the same reusable workflow but with different
trigger topologies (chained-by-`needs` on PR, chained-by-`workflow_run`
on main).

## 9. Extending support

### 9.1 Adding a new gated field

1. Edit the PKL schema under `schema/pkl/main/` — add the field with
   `@K8sVersion { introducedIn = "1.X"; reference = "..." }`.
2. If the field is one Formae will preflight-check, add a corresponding
   entry to `pkg/k8sversion/registry.go`.
3. (Optional) Drop a fixture under
   `testdata/main/features/<feature-name>/` with a `meta.pkl` declaring
   `minK8sVersion = "1.X"`.
4. Run `make generate-versioned-schemas`. Commit `schema/pkl/main/`,
   `schema/pkl/generated/`, `testdata/main/`, `testdata/generated/`,
   and `pkg/k8sversion/registry.go` together.

### 9.2 Adding a new K8s minor

1. Add the minor to `tools/gen-versioned-reflect/versions.pkl`.
2. Add the corresponding row to the KinD image table — see the next
   step.
3. Run `make generate-versioned-schemas` and commit the new
   `schema/pkl/generated/v<minor>/` and `testdata/generated/v<minor>/`
   trees.
4. Create `.github/workflows/conformance-v1-XX.yml` for the new minor:
   - The newest minor: `on: push: { branches: [main] }, pull_request,
     workflow_dispatch`.
   - Any older minor: `on: workflow_run: { workflows: ["Conformance K8s
     1.<next-newer>"], types: [completed], branches: [main] },
     workflow_dispatch` plus an `if: workflow_run.conclusion ==
     'success'` gate.
5. Add a job to `.github/workflows/conformance-pr.yml` with `needs:
   [v1_<next-newer>]`.
6. Add a status badge to `README.md` under
   *Conformance per K8s version*.

### 9.3 Dropping an EOL minor

1. Remove the minor from `versions.pkl`.
2. Run `make generate-versioned-schemas` (deletes the generated
   subtree).
3. Delete `testdata/generated/v<minor>/`.
4. Delete `.github/workflows/conformance-v1-XX.yml`.
5. Update the next-older workflow's `workflow_run` trigger to point at
   the next-newer remaining workflow.
6. Remove the badge.

### 9.4 KinD image table

KinD release ↔ K8s minor mapping (refresh the entry in the workflow
file when bumping):

| K8s | KinD release | node_image |
|-----|-------------|------------|
| 1.34 | v0.31.0 | kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48 |
| 1.33 | v0.31.0 | kindest/node:v1.33.7@sha256:d26ef333bdb2cbe9862a0f7c3803ecc7b4303d8cea8e814b481b09949d353040 |
| 1.32 | v0.31.0 | kindest/node:v1.32.11@sha256:5fc52d52a7b9574015299724bd68f183702956aa4a2116ae75a63cb574b35af8 |
| 1.31 | v0.31.0 | kindest/node:v1.31.14@sha256:6f86cf509dbb42767b6e79debc3f2c32e4ee01386f0489b3b2be24b0a55aac2b |
| 1.30 | v0.29.0 | kindest/node:v1.30.13@sha256:397209b3d947d154f6641f2d0ce8d473732bd91c87d9575ade99049aa33cd648 |
| 1.29 | v0.27.0 | kindest/node:v1.29.14@sha256:8703bd94ee24e51b778d5556ae310c6c0fa67d761fae6379c8e0bb480e6fea29 |
| 1.28 | v0.25.0 | kindest/node:v1.28.15@sha256:a7c05c7ae043a0b8c818f5a06188bc2c4098f6cb59ca7d1856df00375d839251 |
| 1.27 | v0.25.0 | kindest/node:v1.27.16@sha256:2d21a61643eafc439905e18705b8186f3296384750a835ad7a005dceb9546d20 |
| 1.26 | v0.25.0 | kindest/node:v1.26.15@sha256:c79602a44b4056d7e48dc20f7504350f1e87530fe953428b792def00bc1076dd |
| 1.25 | v0.22.0 | kindest/node:v1.25.16@sha256:e8b50f8e06b44bb65a93678a65a26248fae585b3d3c2a669e5ca6c90c69dc519 |
| 1.24 | v0.22.0 | kindest/node:v1.24.17@sha256:bad10f9b98d54586cba05a7eaa1b61c6b90bfc4ee174fdc43a7b75ca75c95e51 |
| 1.23 | v0.22.0 | kindest/node:v1.23.17@sha256:14d0a9a892b943866d7e6be119a06871291c517d279aedb816a4b4bc0ec0a5b3 |
| 1.22 | v0.20.0 | kindest/node:v1.22.17@sha256:f5b2e5698c6c9d6d0adc419c0deae21a425c07d81bbf3b6a6834042f25d4fba2 |
| 1.21 | v0.20.0 | kindest/node:v1.21.14@sha256:8a4e9bb3f415d2bb81629ce33ef9c76ba514c14d707f9797a01e3216376ba093 |

## 10. Open follow-ups

These were deliberately deferred from this PR:

- **PKL ↔ Go drift detector.** Make CI fail when a `@K8sVersion`
  annotation in PKL has no matching `pkg/k8sversion/registry.go`
  entry (and vice versa). Today they're enforced by code review only.
- **Per-feature conformance in CI.** The features matrix was excluded
  after `service-traffic-distribution` exposed a Service-update bug
  (Update of `trafficDistribution = ""` never reaches a terminal state
  — see PR #45 commit history). Re-enable once that's fixed.
- **Charts in CI.** Moved to `testdata/main/charts/` and excluded from
  generation. They're kept for a separate use case (Helm-style
  deploys); CI integration is a separate effort.
- **`workflow_run` chain on main is one-way.** A regression on K8s 1.30
  doesn't surface as a 1.34 badge failure — each badge tracks only its
  own version. That's intentional (so 1.34 stays green even if older
  versions break), but readers should mentally combine the badges to
  judge "is the plugin healthy across the support window".
- **Automatic bumping.** Adding a new K8s minor still requires a
  `versions.pkl` edit and a hand-written workflow file. A scheduled
  job that opens a PR for each new K8s release would close the loop.

## 11. References

- PR #45 — `feat/k8s-version-annotations` — implementation
- `schema/pkl/main/k8s.pkl` — `class K8sVersion`
- `tools/gen-versioned-reflect/main.go` — schema generator
- `tools/gen-versioned-testdata/main.go` — testdata generator
- `pkg/k8sversion/registry.go` — runtime preflight registry
- `.github/workflows/conformance-version.yml` — reusable workflow
- `.github/workflows/conformance-pr.yml` — PR chain
- `.github/workflows/conformance-v1-*.yml` — per-version entry points
