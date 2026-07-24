# Changelog

All notable changes to the formae Kubernetes plugin are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Install with `sudo formae plugin install k8s` on the host that runs the
formae agent.

## [0.1.8]

Requires formae >= 0.86.0.

### Fixed

- **Unreachable clusters are now reaped.** A resource `Read` against an
  unreachable apiserver (connection refused, DNS failure, dial/read timeout)
  fell through to a raw error the host rendered as `UnforeseenError`, which
  carries no health signal — so the target reaper never saw the cluster as
  unreachable and never reaped it. The `Read` funnel now maps a genuine
  client-side transport failure to `NetworkFailure`/`ServiceTimeout`.
  Auth/credential failures are deliberately excluded: a bad token surfaces as a
  `*url.Error` that satisfies `net.Error` even though the apiserver is
  reachable, so classification unwraps to concrete `net` types and skips the
  auth path — a healthy cluster is never reaped over a bad credential.

### Changed

- **`client-go` bumped to `v0.36.3`** (`k8s.io/api` and `k8s.io/apimachinery`
  moved in lockstep), staying pinned to the highest supported K8s minor (1.36).
- **Builds on Go 1.26.** Workflow `go-version` pins now match the `go 1.26.0`
  declared in `go.mod`.
- **Examples pinned to formae 0.88.0.** The runtime requirement is unchanged —
  `minFormaeVersion` remains 0.86.0.

## [0.1.7]

Requires formae >= 0.86.0.

### Changed

- **Breaking: pod and job template metadata no longer carries `name`/`namespace`.**
  The metadata of pod and job templates (`spec.template.metadata` on `Deployment`,
  `StatefulSet`, `DaemonSet`, `ReplicaSet` and `Job`, and `spec.jobTemplate.metadata`
  on `CronJob`) changes type from `ObjectMeta` to `PodTemplateMetadata`, which holds
  only `labels` and `annotations`. Kubernetes accepts `name`/`namespace` on templates
  but never uses them (pods get generated names), so the schema previously forced you
  to invent a value with no effect. Formas that construct the metadata class explicitly
  inside a template need a one-line migration: swap
  `new k8s.ObjectMeta { name = "..."; labels { ... } }` for
  `new k8s.PodTemplateMetadata { labels { ... } }` and drop the `name`. Because the
  cluster stored the template name, the first apply after upgrading that includes the
  pod template removes that stored field, changing the template hash and performing one
  rolling update of the workload, so plan for it as you would any rolling restart.
  Unaffected: the amend style (`metadata { labels { ... } }`), top-level resource
  `metadata` (its `name` is still required), and StatefulSet `volumeClaimTemplates` metadata.

### Fixed

- **Discovery no longer skips workloads created outside formae.** Discovery validated
  foreign manifests against the authoring schema, which required
  `spec.template.metadata.name`, a field Kubernetes ignores and virtually no Helm chart
  or kubectl-applied manifest sets. As a result most Deployments, StatefulSets and
  CronJobs created outside formae were silently missing from discovery: no error surfaced,
  the resources simply never appeared in the inventory. With the template-metadata change
  above, these workloads are discovered and can be brought under management.

## [0.1.6]

Requires formae >= 0.86.0.

### Added

- **Custom resources (CRD instances).** The plugin can now manage any custom
  resource (instances of CRDs like cert-manager `Certificate`, Argo
  `Application`, Flux `GitRepository`, and so on) with no per-CRD Go code or
  generated schema, through a generic catch-all type. `K8S::Custom::Resource`
  reads `apiVersion`/`kind` from the manifest, resolves the GVR against the live
  cluster, and applies via Server-Side Apply, the same mechanics as the built-in
  typed resources. The body (`spec` and any top-level fields) is free-form, so it
  works for arbitrary CRD schemas. This is an escape-hatch model: no field-level
  validation or autocomplete, by design. Identity is a composite `formaeId`
  (`<apiVersion>/<kind>/<namespace>/<name>`), unique across kinds since a single
  type spans every CRD.
- **CustomResourceDefinitions as a first-class type.**
  `K8S::Apiextensions::CustomResourceDefinition` manages CRDs themselves (a CRD
  is just an `apiextensions.k8s.io/v1` object). Backed by the same generic
  provisioner, it lets a CRD and an instance of the kind it defines live in one
  forma and deploy in a single `formae apply`, with no `kubectl` bootstrap. The
  CRD provisioner blocks until the CRD's `Established` condition is `True`, and
  the instance's apply retries (re-discovering the RESTMapper) until its kind is
  served, so the two converge in one apply with no explicit ordering, and survive
  destroy/recreate cycles.
- **Install operators with Helm, end-to-end.** `HelmChart` now maps
  chart-rendered kinds that have no typed provisioner (the CRDs an operator
  ships) through `K8S::Custom::Resource` instead of skipping them. A single
  `HelmChart` therefore installs a complete operator: controllers and RBAC as
  typed resources, the operator's CRDs via the catch-all.

> **Discovery is currently disabled.** Both `K8S::Custom::Resource` and
> `K8S::Apiextensions::CustomResourceDefinition` are marked `discoverable = false`.
> A single catch-all type spans every CRD kind, so unscoped discovery would pull
> every custom resource on the cluster (including operator-internal churn) into
> inventory. Discovery stays off until a scoped design lands; CRUD of
> explicitly-declared resources is fully supported.

### Fixed

- **Helm: workloads ordered after their ServiceAccount.** Helm-rendered
  workloads set `serviceAccountName` as a plain string, so formae applied
  Deployments concurrently with their ServiceAccounts, and Pods then failed with
  `serviceaccount not found` until the SA caught up. The mapper now emits
  `serviceAccountName` as a resolvable referencing the SA resource, so formae
  creates the ServiceAccount first and resolves the name. The reference
  round-trips to the SA name, so there is no drift. (The cluster-default
  `default` SA is left a plain string to avoid a dangling reference.)
- **Helm: invalid `JobSpec.restartPolicy`.** The Helm `batch` mapper emitted
  `restartPolicy` at the `JobSpec` level, where the field does not exist (it
  belongs on the pod template). Charts that ship a `Job` (e.g. install hooks) now
  render correctly.
- **Discovery no longer errors on resource types newer than the target cluster.**
  The plugin registers every resource type, but a type can be newer than the
  target cluster's Kubernetes version, e.g. `MutatingAdmissionPolicy` (GA in
  1.36) on a 1.33 cluster. Discovery still called `List` for it, the apiserver
  returned `the server could not find the requested resource`, and it was logged
  on every discovery pass. Operations are now gated on whether the type is served
  by the target's Kubernetes version. The plugin resolves the target's version
  and, for a type that version doesn't serve, handles each operation accordingly:
  discovery `List` returns empty (no error), `Create`/`Update` fail with a clear
  message naming the type and version, `Read`/`Status` report not-found, and
  `Delete` is a no-op. Resolution is fail-safe: if the cluster version can't be
  determined, operations proceed as before.

## [0.1.4]

Requires formae >= 0.86.0.

### Added

- **K8s 1.36 conformance landed.** `kind` v0.32.0 ships `kindest/node:v1.36.1`,
  so K8s 1.36 now runs the full conformance suite instead of being schema-only.
  The per-minor chain on `main` extends to 1.36 down to 1.21, the PR conformance
  suite exercises 1.36 against `kindest/node:v1.36.1`, and the nightly cluster
  moves to the same image. This closes the 1.36 wire-up tracked in 0.1.3.
- **MutatingAdmissionPolicy.** New resource type
  `K8S::Admissionregistration::MutatingAdmissionPolicy`
  (`admissionregistration.k8s.io/v1`, GA in K8s 1.36,
  [KEP-3962](https://kep.k8s.io/3962)), the in-tree, CEL-based successor to
  mutating admission webhooks. It supports the full CRUD lifecycle and models
  `matchConstraints`, `variables`, `matchConditions`, and `ApplyConfiguration` /
  `JSONPatch` mutations. The whole module is gated `introducedIn = "1.36"`, so it
  materializes only in the `@k8s/v1.36+` schema trees; referencing it with
  `kubernetesVersion` set to an earlier minor fails at `pkl eval` time. This
  raises the supported resource count to 36 types across 13 API groups.

### Changed

- **Supported version window (clarification).** Two version ranges are in play,
  and they are not the same. Schema trees ship for 1.21 to 1.36 (16 minors),
  giving typed Pkl authoring and `pkl eval`-time field validation for that whole
  range. The runtime support window is 1.31 to 1.36 (`MinSupportedK8sVersion` /
  `MaxSupportedK8sVersion` in `pkg/config/version.go`); applying against a live
  cluster outside this window returns a clear preflight error. So schema trees
  exist for 1.21 to 1.30, but those minors are below the runtime floor: you can
  author and eval against them, yet the plugin will refuse to drive a live
  cluster older than 1.31. The floor has been 1.31 since the per-version schema
  system was introduced; only the ceiling has moved (1.34 to 1.36, in lockstep
  with the pinned `client-go`). Targeting a cluster below 1.31 is not supported.

## [0.1.3]

Requires formae >= 0.86.0.

### Added

- **K8s 1.35 and 1.36 supported.** The schema package now ships per-version
  subtrees for K8s minors 1.21 to 1.36 (16 minors). Target a specific cluster's
  API version through the `kubernetesVersion` field on the K8s `Config`. Each
  per-version subtree under `@k8s/v<X.Y>/` carries only fields that are valid for
  that minor; formae evaluates against the matching subtree at extract and apply
  time, surfacing field-availability errors before any RPC reaches the cluster.
  The `client-go` dependency is pinned to `v0.36.0` in lockstep with the highest
  supported minor. The plugin's `pkg/config/version.go` records
  `MinSupportedK8sVersion = "1.31"` and `MaxSupportedK8sVersion = "1.36"`; users
  on a cluster outside that window get a clear preflight error.
- **Service LoadBalancer resolvables.** `core/Service` now exposes its assigned
  LoadBalancer endpoint through resolvables: `lbIngressIp`, `lbIngressHostname`,
  and `lbIngressUrl`. The URL form is synthesized by the plugin as
  `http://<host>[:port]` from the first ingress address and the first service
  port, so a cross-plugin Target can take its endpoint directly from a `$ref` on
  the Service.
- **lgtm-observability example.** New example composing three plugins in a single
  forma: a cloud plugin provisions a managed cluster (AWS, Azure, GCP, or OCI),
  the k8s plugin deploys the LGTM stack onto it, and the grafana plugin
  configures Grafana (folder, data sources, dashboards) over its HTTP API. Target
  chaining wires it together: the K8s target's auth is a `$ref` on the cluster
  endpoint, and the Grafana target's URL is a `$ref` on the Grafana Service's
  LoadBalancer ingress URL.
- **Versioned conformance CI.** A new `Conformance K8s 1.35` workflow runs on
  every push to `main`, joining the existing per-minor chain (`1.35 → 1.34 → ...
  → 1.21`). The PR conformance suite now exercises 1.35 against
  `kindest/node:v1.35.1`. K8s 1.36 conformance is tracked separately and will
  land once `kind` publishes a 1.36 node image; see
  [issue #9](https://github.com/platform-engineering-labs/formae-plugin-kubernetes/issues/9)
  for the wire-up checklist.

### Changed

- **Schema layout.** The published `schema/pkl/` tree now splits responsibility
  cleanly between the package root and the per-version subtrees. `target.pkl` is
  the version-agnostic package root, carrying `Config` (including
  `kubernetesVersion`) and the `Auth` hierarchy (`KubeconfigAuth`, `EKSAuth`,
  `GKEAuth`, `AKSAuth`, `OVHAuth`, `OCIAuth`). `v<X.Y>/k8s.pkl` is the per-version
  SubResource module (`PodSpec`, `Container`, `EnvVar`, `ObjectMeta`, and every
  other inline type whose accepted field set varies per K8s minor); each
  per-version file `extends "../target.pkl"`, so importing `@k8s/v1.34/k8s.pkl`
  also gives you `Config` + `Auth` via inheritance. Resource files
  (`@k8s/v<X.Y>/<api-group>/<Kind>.pkl`) sit under each per-version subtree and
  import the matching `k8s.pkl` for their subresource dependencies.

### Fixed

- **Example formae files.** The example `forma.pkl` files under
  `examples/formations/` now import `@k8s/k8s-subresources.pkl as k8s` against the
  master schema, restoring chart-conformance test compatibility that broke during
  the earlier schema split. Per-version example files
  (`examples/helm/{nginx,memcached,postgresql}-v1.{31,34}.pkl`) import the
  per-version `@k8s/v<X.Y>/k8s.pkl` subresources file.

## [0.1.2]

### Added

- Initial release of the Kubernetes plugin as a standalone package on the
  platform.engineering Hub. Manages K8s resources via Server-Side Apply with
  typed Pkl schemas pinned to your cluster's K8s version.
