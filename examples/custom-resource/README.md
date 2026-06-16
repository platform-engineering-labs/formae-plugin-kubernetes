# Custom Resource (CRD instance) example

Demonstrates managing CustomResourceDefinitions and their instances with formae,
no kubectl. Two resource types do the work:

- **`K8S::Apiextensions::CustomResourceDefinition`** — a CRD (dedicated type).
- **`K8S::Custom::Resource`** — the generic catch-all for any custom-resource
  instance, or any K8s kind without a typed provisioner.

Both are backed by one generic dynamic provisioner (it reads `apiVersion`/`kind`
from the manifest and applies via Server-Side Apply), so **no per-CRD Go code or
pkl is required**.

## Files

- `crd-and-widget.pkl` — deploys the `widgets.example.com` CRD **and** a `Widget`
  instance in a single `formae apply`.
- `config/vars.pkl` — stack + target. Note `customResourceGroups = { "example.com" }`,
  the opt-in allowlist that makes `Widget` instances discoverable.

## Identity

One catch-all type spans every CRD kind, so `metadata.name` is not unique.
Identity is the composite `formaeId` (`<apiVersion>/<kind>/<namespace>/<name>`),
computed identically in pkl and in the Go provisioner; the same string is also
the plugin NativeID.

## Run

```bash
make install                           # build + install the plugin
pkl project resolve examples/custom-resource
formae apply --mode reconcile --yes --watch examples/custom-resource/crd-and-widget.pkl

# verify
kubectl get crd widgets.example.com
kubectl get widgets -n default
formae inventory resources --query "stack:custom-resource-demo managed:true"

# teardown
formae destroy --yes examples/custom-resource/crd-and-widget.pkl
```

formae applies the CRD and the Widget concurrently; the CRD provisioner blocks
until the CRD is Established and the Widget's apply retries until its kind is
served, so the Widget converges without an explicit dependency edge.

> Bump the `@k8s/v1.33/...` imports here and `kubernetesVersion` in
> `config/vars.pkl` to match your cluster's Kubernetes minor (`kubectl version`).
