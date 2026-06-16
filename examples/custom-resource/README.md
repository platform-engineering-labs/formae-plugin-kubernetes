# Custom Resource (CRD instance) example & conformance fixture

Demonstrates the generic `K8S::Custom::Resource` catch-all type, which manages
any custom resource (CRD instance) — or any K8s kind without a typed
provisioner, including `CustomResourceDefinition` itself — via Server-Side Apply
and a discovery-backed RESTMapper. No per-CRD Go code or pkl is required.

## Files

- `crd.yaml` — the `widgets.example.com` CRD (group `example.com`, namespaced
  `Widget` kind). Prerequisite for the conformance fixture below.
- `custom-resource.pkl` — a `Widget` instance declared as `K8S::Custom::Resource`.
- `custom-resource-update.pkl` — the update variant (`spec.size` 3 → 7).
- `config/vars.pkl` — stack + target. Note `customResourceGroups = new { "example.com" }`,
  the opt-in allowlist that makes `Widget` instances discoverable.

## Identity

A single catch-all type spans every CRD kind, so `metadata.name` is not a unique
identifier. Identity is the composite `formaeId`
(`<apiVersion>/<kind>/<namespace>/<name>`), computed identically in pkl and in
the Go provisioner; the same string is also the plugin NativeID.

## Running the conformance test

The CRUD conformance harness expects exactly one resource per fixture, so the
CRD is provisioned out-of-band as a prerequisite (formae itself can also deploy
the CRD via `K8S::Custom::Resource` — the catch-all handles `CustomResourceDefinition`
too — but that is a second resource and belongs in its own apply).

```bash
# 1. Install the plugin under test
make install

# 2. Create the prerequisite CRD
kubectl apply -f examples/custom-resource/crd.yaml
kubectl wait --for=condition=Established crd/widgets.example.com --timeout=30s

# 3. Run CRUD + discovery conformance against this fixture
export FORMAE_BINARY=$(command -v formae)
export FORMAE_TEST_TESTDATA_DIR=$(pwd)/examples/custom-resource
export FORMAE_TEST_FILTER=custom
export FORMAE_K8S_VERSION=1.33   # match your cluster minor
go test -tags=conformance -v -timeout 10m .

# 4. Clean up
kubectl delete -f examples/custom-resource/crd.yaml
```

The CRUD pass exercises create → verify → extract → sync → update → destroy →
out-of-band-delete; the discovery pass creates an out-of-band `Widget`,
discovers it via the opt-in group allowlist, and verifies the discovered
identity.

> Bump the `@k8s/v1.33/...` imports in `custom-resource.pkl` (and the
> `k8sMinor` in `config/vars.pkl`) to match your cluster's Kubernetes minor.
