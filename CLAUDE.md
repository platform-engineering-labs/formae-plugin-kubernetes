# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

Kubernetes resource plugin for [formae](https://github.com/platform-engineering-labs/formae).
Manages K8S resources via Server-Side Apply (SSA). The plugin binary communicates
with the formae agent over the plugin SDK protocol.

## Build & Test Commands

```bash
make build              # Build plugin binary to bin/k8s
make install            # Build + install to ~/.pel/formae/plugins/k8s/
make lint               # golangci-lint
make test-unit          # Unit tests (//go:build unit)
make test-integration   # Integration tests (//go:build integration), needs a K8S cluster

# Run a single integration test
go test -v -tags=integration -run TestConfigMapCRUDLifecycle ./pkg/resources/core/...

# Conformance tests (full CRUD + discovery via formae CLI, needs cluster + formae binary)
make conformance-test                        # All
make conformance-test TEST=namespace         # Filter by name
make conformance-test-crud                   # CRUD only
make conformance-test-discovery              # Discovery only
make conformance-test-resources              # Non-chart resources only
make conformance-test-charts                 # Chart-based tests only

# Conformance test parameters
make conformance-test PARALLEL=10            # Run tests in parallel
make conformance-test TIMEOUT=10             # Timeout in minutes (default: 5)
make conformance-test FORMAE_BINARY=/path    # Override formae binary location

# Other test targets
make chart-test                              # Helm chart smoke tests (deploy + verify + cleanup)
make chart-test CHART=nginx                  # Filter by chart name
make drift-test                              # Drift detection + reconciliation test

# Schema generation (PKL schemas from pkl-k8s reflection)
make generate-schema
make verify-schema
```

## Architecture

### Plugin Entry Point

`main.go` boots the plugin via `sdk.RunWithManifest`. `k8s.go` defines the
`Plugin` struct implementing `plugin.ResourcePlugin`. All CRUD+List calls
delegate to a type-specific `Provisioner` looked up from the registry.

### Resource Registration Pattern

Each resource type lives in its own file under `pkg/resources/<apigroup>/`
(e.g., `core/configmap.go`, `apps/deployment.go`). Every file follows the same
pattern:

1. An `init()` function calls `registry.Register(resourceType, operations, factory)`
2. The resource type constant uses the convention `K8S::<Group>::<Kind>`
   (e.g., `K8S::Core::ConfigMap`, `K8S::Apps::Deployment`)
3. Blank imports in `k8s.go` trigger all `init()` registrations

### Two Provisioner Strategies

- **Typed provisioners** (most resources): Use client-go typed clients and
  `ApplyConfiguration` types. Create/Update use SSA via `.Apply()`. LiveState
  is computed by round-tripping through the apply config type to strip
  server-managed fields (`prov.LiveState[T]`).

- **Generic provisioner** (`pkg/resources/generic/customresource.go`): Uses the
  dynamic client + `unstructured.Unstructured` for CRDs. Resources register via
  `generic.RegisterCRD()` with a `schema.GroupVersionResource`.

### Key Packages

- `pkg/resources/prov/` - Shared provisioner logic:
  - `LiveState[T]` - Strips server-managed fields from API responses
  - `ReconcileMetadata` - Removes labels/annotations not in desired state (on Update)
  - `NativeID` / `ParseNativeID` - Namespaced resource IDs (`namespace/name`)
  - `NativeIDWithUID` - Cluster-scoped resource IDs (`name:uid`)
- `pkg/resources/registry/` - Global provisioner registry (thread-safe map)
- `pkg/config/` - Target config parsing (kubeconfig, direct endpoint/EKS auth)
- `pkg/transport/` - K8S client wrapper (typed + dynamic + apiextensions clients)
- `pkg/resources/testutil/` - Integration test helpers (`SetupEnv`, `RunCRUDLifecycle`)

### Server-Managed Field Stripping

`prov/livestate.go` is critical for correctness. It strips fields that K8S
controllers inject (imagePullPolicy, serviceAccount volumes, system labels, etc.)
to prevent false drift detection. When adding a new resource type, you may need
to extend `stripControllerInjectedFields` if the resource has kind-specific
server defaults.

### PKL Schema

`schema/pkl/` contains PKL type definitions generated from `pkl-k8s` via
`tools/gen-schema/generator.pkl`. These define the user-facing forma resource
types. Regenerate with `make generate-schema`.

### Helm Charts

`helm/` contains a codegen system that generates forma files from Helm charts.
The `charts/` directory defines specific chart configurations (Nginx, Grafana,
etc.) used in conformance tests.

### Test Types

- **Integration tests** (`//go:build integration`): Test provisioners directly
  against a live K8S cluster. Use `testutil.SetupEnv` which creates an isolated
  namespace (hardcoded to `orbstack` context). Located alongside resource code.
- **Conformance tests** (`//go:build conformance`): Black-box tests via the
  formae CLI exercising the full CRUD + discovery lifecycle. Defined in
  `conformance_test.go`, test data in `testdata/`.

### NativeID Conventions

- Namespaced resources: `namespace/name`
- Cluster-scoped resources: `name` or `name:uid` (uid for delete+recreate disambiguation)
