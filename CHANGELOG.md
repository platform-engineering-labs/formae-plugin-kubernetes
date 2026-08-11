# Changelog

All notable changes to the formae Kubernetes plugin are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Install with `sudo formae plugin install k8s` on the host that runs the
formae agent.

## [Unreleased]

### Removed

- **`HelmChart.pkl` and its per-version wrapper trees are gone** — 281 files,
  ~39k lines: `schema/pkl-helm/` (the `HelmChart` module, 17 api-group mappers
  and the `gen-versioned-helm` codegen) and the generated
  `schema/pkl/helm/v1.21…v1.36/` trees, plus the `make` targets
  `generate-versioned-helm-schemas`, `verify-helm-schemas` and the CI job that
  ran the latter.

  **Breaking for anyone importing `@k8s/helm/v<X.Y>/HelmChart.pkl`.** Migrate to
  `K8S::Helm::Release`: one resource per chart, Helm applies the objects. There
  is no mechanical rewrite — `HelmChart` produced N typed resources in formae
  state and `Release` produces one, so the release adopts what the chart already
  installed rather than inheriting per-object state.

  It was removed rather than deprecated because it could not honour hooks.
  `helm template` emits hook-annotated manifests with no orchestration, so a
  `pre-install` Job became a permanent resource that never re-ran, `hook-weight`
  was ignored, finished hooks accumulated, and `test` hooks were applied on every
  reconcile. Charts that relied on hooks applied silently wrong, which is a worse
  failure than not being supported.

  Removing it also drops the `pkl-readers/helm@0.1.2` package dependency and the
  `pkl-reader-helm` external-reader declaration from the example projects:
  nothing renders a chart at Pkl-eval time any more.

  Deleted with it: the seven `HelmChart`-based single-file examples
  (`nginx*.pkl`, `postgresql-v1.31.pkl`, `memcached-v1.31.pkl`,
  `create-namespace-test.pkl`, `imagepullsecrets-test.pkl`) and two already-dead
  `make` targets — `chart-test`, whose script had been removed, and
  `conformance-test-charts`, whose `*-chart` filter matched no remaining fixture.
  `examples/flux/flux-helm.pkl` is ported to a `Release`, so Flux still installs
  with one `formae apply`.

### Added

- **`K8S::Helm::Release` — Helm charts driven by the embedded Helm SDK.** Formae
  manages the release; Helm manages the objects the chart renders. Hooks, hook
  weights, hook delete policies, CRD install ordering and revision history all
  work because Helm implements them, not because the plugin reimplements them.
  The release is a genuine Helm release, so `helm list`, `helm history` and
  `helm rollback` see it.

  `Create`/`Update` submit with `Wait=false` and return `InProgress` once Helm
  has written the release record; `Status` polls the record and then checks every
  rendered object with Helm's own `ReadyChecker`. The plugin stores nothing — all
  state lives in the release Secret and the cluster.

  **A release is only recorded by formae once it is fully deployed.** `Create`
  returns no `NativeID`, so a half-installed release is never put under
  management; `Status` supplies it after the release reaches `deployed` and every
  rendered object passes readiness. The `RequestID` carries namespace, name and
  target revision, and is what `Status` uses to find the release meanwhile.
  `Update` is exempt — the resource is already in state.

  Consequence to be aware of: a first install that fails leaves formae with no
  handle on the release, so `formae destroy` cannot clean it up. Re-applying the
  same forma does recover it — see the crash-recovery entry below.

  Complements `HelmChart.pkl`, which renders client-side and decomposes into
  typed resources. Prefer `Release` for charts with hooks, CRDs or subcharts;
  prefer `HelmChart` when per-object formae state matters more than chart
  fidelity.

- **A crash mid-install no longer needs manual recovery.** Helm has no
  server-side operation controller: the install runs in the plugin process, so
  when that process dies the work dies with it, and Helm's `pending-install`
  status is left behind as a lock with no owner. Helm refuses both install and
  upgrade on a release in that state, and its documented way out is
  `helm uninstall` — which destroys the objects and re-runs `pre-install` hooks
  to get back to where it already was.

  The plugin now clears that lock itself on the next operation, doing what
  Helm's own `failRelease` does: set the status, write the record back. Whether
  it settles on `deployed` or `failed` is decided by the cluster, because Helm
  writes `deployed` last — after the hooks have run and every object exists:

  ```
  Releases.Create(pending-install) -> hooks -> create objects -> SetStatus(deployed)
  ```

  Dying anywhere in that middle stretch leaves an identical record, whether the
  work finished or never started. So recovery checks: every object the release
  renders present and ready means the install did complete and only the record
  was lost — it is recorded `deployed` and reported as success, with no second
  Helm operation and no hooks re-run. Anything missing means `failed`, which an
  upgrade is allowed to run over, three-way merging what is absent.

  The guarantee, whatever died:

  | | |
  |---|---|
  | The command reaches a verdict | It never sits in `InProgress` reporting work nothing is doing |
  | A failure says why | e.g. `objects are incomplete (ConfigMap/app-config is absent)` |
  | The next apply converges | No operator, no `helm uninstall` |

  Two limitations worth knowing before you rely on this:

  - **A plugin crash defers recovery to the next apply.** The agent's
    PluginOperator runs on the plugin's node and dies with it, so the agent
    fails the command without calling `Status` again and the plugin never gets
    the chance to clear the record. The release stays `pending-install` until
    something applies again — which then recovers it automatically.
  - **That failure currently carries no message**, because it comes from the
    agent's own missing-in-action path rather than from the plugin.

  A release this plugin did not install is never rewritten. Someone else's stuck
  operation is reported, with the recovery command in the message, and left
  alone.

- **An interrupted operation is recognised rather than repeated.** A re-driven
  `Create` against a release that is already at the desired version and values
  runs no Helm operation at all — it only re-checks readiness. Previously it
  planned an upgrade, bumping the revision for nothing and re-firing
  `pre-upgrade` and `post-upgrade` hooks; on a chart like kratos that is a
  second database migration.

  A duplicate `Create` that reaches the *same* plugin process while its install
  is still running rejoins that operation and returns the same `RequestID`,
  guarded on a fingerprint of the desired state — a call carrying a *different*
  change still gets `ResourceConflict`. Note this cannot help across an agent
  restart: a restarted agent never reattaches to a surviving plugin, so the
  re-driven call always lands on a process whose in-flight registry is empty.

- **In-flight operations are cancelled on `SIGTERM`**, so Helm records `failed`
  rather than leaving `pending-install`. This covers a signal sent directly to
  the plugin — a container runtime stopping the pod, systemd, an operator. It
  does **not** cover `formae agent stop`, which reaches the plugin as `SIGKILL`
  via the supervisor's port, leaving no opportunity to unwind.

- **A release is no longer declared dead while it is still installing.** Whether
  an operation is running is now a fact rather than a guess: Helm operations for
  formae run in exactly one process, so if this plugin is not running one for a
  release, nothing is. Recovery is therefore immediate — a release abandoned
  seconds ago is as recoverable as one abandoned an hour ago — and a release this
  plugin *is* installing is never touched, however long its hooks take.

  A clock is still needed for releases this plugin does not own, where another
  tool may legitimately be mid-operation and we cannot see it. That window now
  comes from the operation's own timeout, recorded on the release as
  `formae.dev/timeout-seconds`, instead of a fixed 20 minutes that declared a
  release with `timeoutSeconds = 1800` dead while it was still running.

- **Ownership guard on adoption.** A release this plugin did not install is no
  longer taken over silently. Every release formae installs carries a
  `formae.dev/managed=true` Helm release label; Helm carries release labels
  forward across `upgrade` and `rollback`, so the marker identifies a release
  lineage rather than one revision. Applying a forma over a release without it
  fails with instructions to adopt via `formae extract` instead. The marker is
  also what lets a failed first install be retried — formae withholds the
  NativeID until a release is deployed, so the retry arrives as a create.

- **Delete waits for the objects to actually go.** It set `Wait = false` and
  returned `Success` as soon as Helm accepted the uninstall, so a
  destroy-then-apply could race objects still terminating. It now fires the
  uninstall with `Wait = true` and returns `InProgress`; `Status` treats the
  release record's absence as completion, which Helm's ordering makes exact — the
  record is purged only after `WaitForDelete` and the post-delete hooks finish.
  This also wires up the previously dead `:delete` request-id path, and reports a
  stalled uninstall instead of hanging.

- **`repoURL` on `K8S::Helm::Release`.** Set it alongside a bare chart name
  (`repoURL = "https://k8s.ory.sh/helm/charts"`, `chart = "kratos"`) to resolve a
  chart from a classic HTTP repo. Without it a `repo/chart` reference only works
  if someone has run `helm repo add` on the host running the formae agent, which
  a forma cannot depend on. Not needed for `oci://` refs or local paths.

- **Helm's reserved release labels no longer leak.** The secrets driver filters
  them on `Get` but not on the list/last paths, so `Read` was exposing `name`,
  `owner`, `status`, `version` and `modifiedAt`. They were copied into extracted
  formae, and Helm then rejected the next upgrade outright with "user supplied
  labels contains system reserved label name".

- **An `oci://` chart reference no longer drifts.** `Read` reports the chart only
  for a release this plugin did *not* install. Helm records the chart's *name*,
  never the reference that fetched it, so reporting it answered `podinfo` for a
  desired `oci://ghcr.io/stefanprodan/charts/podinfo` and every later plain apply
  was refused as drift. Omitting it lets formae keep the reference the user wrote
  — the treatment `repoURL` has always had.

  Three things had to line up, and each one alone was not enough: `chart` had to
  become optional in the schema (omitting a *required* field makes formae treat the
  resource as invalid and drop it from the inventory), the struct tag needed
  `omitempty` (otherwise the zero value marshals as `"chart":""` and overwrites the
  desired value), and the omission had to be conditional on ownership. A foreign
  release has no desired value to keep, so the chart name is still reported for it
  — without that, discovery records a release with no chart and `formae extract`
  emits a forma that cannot be applied, which breaks adoption.

- **Re-applying the deployed version needs no chart fetch.** Helm stores the whole
  chart in the release record, so when a forma pins the version already deployed
  the plugin reuses it instead of resolving a repository. This is what lets an
  adopted release be managed with no `repoURL` at all — `Read` cannot reconstruct
  one, because Helm does not record where a chart came from. Bumping the version
  still fetches, and still needs `repoURL`. An unpinned `version` always fetches:
  reusing the stored chart there would silently pin the release forever.

- **A bare chart name without `repoURL` is rejected up front**, naming the fix,
  instead of failing inside Helm with "non-absolute URLs should be in form of
  repo_name/path_to_chart". Most often hit when adopting a release installed from
  an HTTP repo: Helm does not record which repository a release came from, so
  `Read` cannot reconstruct `repoURL` and it has to be supplied by hand. `oci://`
  references are self-describing and unaffected.

- **Conformance test coverage for `K8S::Helm::Release`.** `testdata/main/shared/helmrelease{,-update,-replace}.pkl`
  put the resource through the same 24-step CRUD suite and 7-step discovery suite
  every other resource type runs: create, extract round-trip, sync idempotency,
  update, replace via a `createOnly` change, destroy, and out-of-band delete
  detection. Uses podinfo — two objects, no hooks — so the cycle stays quick; the
  hook and adoption behaviour that needs a heavy chart is covered by the
  `helm-drift-test` and `helm-adopt-test` scripts against ory/kratos.

  `make conformance-test` gains an opt-in `K8S_MINOR` parameter that scopes the
  run to one generated testdata tree, matching what `conformance-version.yml` does
  in CI. Unset, the runner walks all of `testdata/` — `main/` plus every generated
  `v1.XX/` tree — so each case runs ~17 times and a filtered run looks like a hang.

- **Helm's release storage no longer surfaces in discovery.** Every revision of
  every release is a Secret of type `helm.sh/release.v1`, so one release at the
  default `MaxHistory` showed up as ten unmanaged Secrets. Excluded via
  `DiscoveryFilters()` rather than the release inventory: that inventory hides
  objects a chart *renders*, and a release Secret appears in no manifest. Only the
  secret driver is covered, which is the one this plugin uses.

- `examples/rollout-safety/` — one folder per case (paused Deployment,
  `OnDelete` StatefulSet, partitioned StatefulSet, HPA coexistence), each with
  `create.pkl`/`update.pkl` and the old-vs-new plugin behavior in the header.

- **CI runs the `integration` tests against a kind cluster**
  (`.github/workflows/integration-pr.yml`). Nothing ran them before: they were
  green only on whichever developer's machine last touched them, and several had
  been red for months against behaviour that had since changed — which is how a
  live uninstall being reported as abandoned reached a release branch. They need
  no formae binary and no agent, only an apiserver, so kind is the whole
  environment.

  Every package is included bar three: `apps`, `batch` and `core` still expect
  `Create`/`Delete` to return `Success` where the plugin returns `InProgress`
  (9 assertions), and un-rotting them is separate work. `test/` keeps its own
  workflows, which need an agent.

  Three things had to change for the suite to be runnable anywhere but the
  machine that wrote it. The stability tests now use their own namespace instead
  of the lifecycle test's, which that test deletes in cleanup — every test
  declared after it failed with `namespace is being terminated`. The kube context
  comes from `KUBE_CONTEXT`, which `pkg/resources/testutil` already read but
  defaulted to one developer's `orbstack`, so 20 tests failed with
  `context "orbstack" does not exist`. And the inventory herd test installs the
  release it needs instead of assuming the cluster holds one: it had been passing
  on the residue earlier runs leave behind, and on a fresh cluster every caller
  correctly returned an empty inventory.

- **Chart-owned objects are collapsed in discovery.** Objects a Helm release
  renders no longer surface as unmanaged alongside the release that owns them.
  Ownership comes from the release's stored manifest rather than the
  `app.kubernetes.io/managed-by: Helm` label, which chart authors are free to
  omit. The inventory is exposed on the release's `resourceNames` so a collapsed
  release still says what it manages.

  Objects created *downstream* of the chart by controllers (the Pods behind a
  Deployment) are not in any manifest and are still discovered individually.

  `resourceNames` is reported **sorted**, and that is load-bearing rather than
  cosmetic. It is reported state, so an order that varies between reads is an
  out-of-band change on every sync, and formae's guard then refuses every apply
  — permanently, with the empty diagnostic of a transient conflict. Any release
  with two objects of one kind is affected, so this is the difference between a
  release being upgradable through formae and not.

### Fixed

- **A live uninstall is no longer reported as abandoned.** `Delete` started its
  Helm uninstall without registering it in the in-flight registry, and
  "a release record this plugin owns with no operation behind it" is exactly how
  an abandoned uninstall is recognised. So the first `Status` poll — 20 seconds
  after `Delete` under the default `statusCheckInterval` — declared every
  uninstall slower than that abandoned, with a recoverable error code that asks
  the agent to re-drive `Delete`, starting a second concurrent uninstall of the
  same release. Slower than 20s is ordinary: a `pre-delete` hook, or `Wait=true`
  sitting through a Pod's `terminationGracePeriodSeconds`.

  Only podinfo-sized charts escaped it, which is why the conformance destroy step
  passed throughout: its record is purged before the first poll. No test chart in
  the repo declares a delete hook, and the kratos scenarios call
  `formae destroy` from a `trap EXIT` cleanup that swallows failures.

- **A release whose objects never become ready now fails instead of polling
  forever.** Under `Wait=false` Helm records `deployed` as soon as the apiserver
  accepts the manifests, and that record never changes again — so a Pod stuck in
  `ImagePullBackOff` from a typo'd tag, or one no node has room for, left `Status`
  answering `InProgress` for eternity. Nothing above caught it either: the agent
  fails an operation when a plugin goes *silent*, never because it keeps
  reporting progress, and there is no cap on how long an operation may run. The
  readiness wait is now bounded by the timeout recorded on the release, the same
  clock that already bounds a pending release, and the failure names the object
  that never came up. An operation this process is still running is exempt, so a
  slow hook is never cut short.

- **An uninstall is bounded by the release's own timeout**, not the package
  default. A release given `timeoutSeconds = 1800` had its uninstall cut off at
  600s while the stalled-release verdict waited twice 1800s before saying so,
  leaving the command `InProgress` for the best part of an hour on work nothing
  was doing.

- **Upgrading a chart with subcharts no longer fails to render.** Re-applying the
  deployed version reuses the chart stored in the release record instead of
  fetching it, but `chart.Chart.dependencies` is unexported and carries no JSON
  tag (`helm/pkg/chart/chart.go:56`), so Helm's own storage drops every subchart
  on the way in — while `Metadata.Dependencies`, which is serialized, goes on
  listing them. Rendering that remnant failed on any helper a dependency defines,
  which for an ory chart is the whole templates directory:

  ```
  template: no template "ory.extraEnvContainsEnvName" associated with template "gotpl"
  ```

  Such a chart is re-fetched now — but only when there is somewhere to fetch
  from. An adopted release has no `repoURL`, because Helm never records where a
  chart came from, so insisting on a fetch there would fail every upgrade of an
  adopted subchart chart outright. With nothing to fetch from, the incomplete
  stored chart is used anyway: rendering it may well succeed, and when a dropped
  subchart really is needed Helm names the template it cannot find. Trading a
  possible failure for a certain one is not an improvement.

  The no-op check that stops a re-driven `Create` re-running hooks is unaffected —
  it compares a version and a set of values and renders nothing, so an incomplete
  stored chart tells it nothing.

- **The plugin's own release labels no longer leak into resource state.**
  `formae.dev/managed` — and now `formae.dev/timeout-seconds` — were reported
  back in `metadata.labels`, which put them into `formae extract` output and made
  them read as drift against a forma that never declared them.

- **Paused Deployments settle instead of polling forever.** A `Deployment` with
  `spec.paused: true` never converges its replica counts — the controller stops
  reconciling by design — so `Status()` reported `InProgress` until the operation
  timed out. A paused Deployment is now `Success` once the apiserver has observed
  the paused spec.
- **StatefulSet `OnDelete` and partitioned rollouts settle.** With
  `updateStrategy.type: OnDelete`, pods are only replaced when deleted by hand, so
  `status.updatedReplicas` never advances and the rollout looked stuck forever.
  `OnDelete` now gates on readiness of the desired set only. With
  `rollingUpdate.partition: N`, only ordinals `>= N` are updated, so the reachable
  updated count is `replicas - partition` (floored at 0) rather than `replicas`.
- **DaemonSet `OnDelete` rollouts settle.** Same root cause as the StatefulSet
  case: `status.updatedNumberScheduled` never reaches
  `desiredNumberScheduled` under `OnDelete`, so status now gates on
  `numberReady` alone.
- **No more false drift against an HPA.** When a `HorizontalPodAutoscaler` scales
  a `Deployment`, `ReplicaSet`, or `StatefulSet`, the HPA — not formae — owns
  `spec.replicas`. formae still read the live count back and reported it as drift
  against a forma that deliberately omitted `replicas`, every reconcile. The
  plugin now consults `metadata.managedFields` and strips `spec.replicas` from
  reported state whenever the `formae` field manager does not own it. A forma
  that *does* declare `replicas` is unaffected — formae owns the field and it
  keeps drifting as before, which is the intended behavior for that case.
- **Over-marked `createOnly` fields no longer force a destructive replace.** Every
  `createOnly` field makes formae plan a delete-then-create replace when the value
  changes. Five fields were marked immutable but are in fact accepted in place by
  the apiserver, so formae was destroying resources for changes Kubernetes would
  have taken: `CSIDriver.spec.requiresRepublish`, `CSIDriver.spec.tokenRequests`
  (both mutable since Kubernetes 1.22), `PriorityClass.globalDefault`,
  `RuntimeClass.overhead`, and `RuntimeClass.scheduling`. Each verdict was
  verified against a live apiserver. The fields that are genuinely immutable keep
  `createOnly`, and their per-field docstrings say so.

### Changed

- **Rollout progress is visible while an operation runs.** The plugin blanked
  `StatusMessage` on every non-`Failure` result, so the per-resource `reason` row
  stayed empty during a rollout. Provisioner messages now pass through on
  `InProgress` (e.g. `replicas: 2/3 ready`) and are blanked only on terminal
  `Success`, where a lingering message is just noise.

## [0.1.10]

### Changed

- Drop the removed `--watch` flag from the example commands in the README,
  CONTRIBUTING, the helm/flux/crossplane/bookstore/custom-resource docs, and
  the example file headers. `formae apply`/`destroy` are submit-then-poll.

## [0.1.9]

### Changed

- Bump examples to the latest formae 0.88.0 schema.

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
