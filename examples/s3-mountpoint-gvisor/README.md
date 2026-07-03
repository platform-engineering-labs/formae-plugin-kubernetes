# Example: storage + sandboxed runtime, one forma per cluster

A single, cross-plugin forma that gives a Pod some storage and (optionally) runs
it under a sandbox runtime like **gVisor**. It adapts to the cluster you point
it at via a **platform profile** — no editing the forma, just an env var.

```bash
PROFILE=orbstack        formae apply --mode reconcile forma.pkl   # local, runs as-is
PROFILE=minikube-gvisor formae apply --mode reconcile forma.pkl   # local gVisor
PROFILE=gke-sandbox     formae apply --mode reconcile forma.pkl   # GKE Sandbox
PROFILE=eks-s3-gvisor   formae apply --mode reconcile forma.pkl   # S3 + gVisor
```

`PROFILE` defaults to `orbstack`. Profiles live in [`platform.pkl`](./platform.pkl).

## What a profile controls

Two orthogonal axes — pick per cluster:

| Axis | Options | Set by |
|------|---------|--------|
| **Storage** | `dynamic` (PVC against a StorageClass) · `s3` (S3 bucket + Mountpoint CSI + static PV) | `storageBackend`, `storageClass` |
| **Runtime** | default runtime · a sandbox RuntimeClass (e.g. gVisor `runsc`) | `runtimeHandler`, `runtimeNodeSelector` |

The forma builds only the resources the profile needs (`when (...)` blocks):

| Profile | Resources emitted |
|---------|-------------------|
| `orbstack` | PVC (local-path) + Pod |
| `minikube-gvisor` | PVC (standard) + RuntimeClass(runsc) + Pod |
| `gke-sandbox` | PVC (standard-rwo) + RuntimeClass(runsc, nodeSelector) + Pod |
| `eks-s3-gvisor` | S3 Bucket + CSIDriver + static PV + PVC + RuntimeClass(runsc) + Pod |

The Pod always mounts the storage at `/data` and writes `demo.txt` to prove it.

## The gVisor bit

gVisor = the `runsc` OCI runtime. A `RuntimeClass` only *references* a handler
the node's containerd already registers — it does **not** install gVisor. So the
sandbox profiles need `runsc` present on a node:

- **Local:** [minikube](https://minikube.sigs.k8s.io) with the gVisor addon —
  the only turnkey local option (kind/orbstack ship no `runsc`):
  ```bash
  minikube start --container-runtime=containerd
  minikube addons enable gvisor        # installs the runsc handler on the node
  PROFILE=minikube-gvisor formae apply --mode reconcile forma.pkl
  kubectl exec app -- cat /data/demo.txt
  ```
  (Check the addon supports your k8s version; it has lagged newer releases.)
- **Cloud:** GKE Sandbox (`gke-sandbox` profile) or a node pool with gVisor
  installed yourself. There is **no** `kindest/node` image that bundles gVisor.

If a profile sets a handler the node doesn't have, the Pod stays
`ContainerCreating` with `FailedCreatePodSandBox: RuntimeHandler "runsc" not
supported`. That's the cluster missing gVisor, not the forma.

## The S3 bit (`eks-s3-gvisor`)

Mountpoint for Amazon S3 is **static-only** — no StorageClass, no dynamic
provisioning. The profile emits a `PersistentVolume` with a `csi:` source
pointing at the bucket, and a PVC with `storageClassName = ""` that pins it via
`volumeName`. Capacity numbers are required by the API but ignored by S3.

Needs, on the cluster: the AWS plugin + creds, the
[Mountpoint S3 CSI driver](https://github.com/awslabs/mountpoint-s3-csi-driver)
with pod/node IAM for the bucket, and gVisor on a node. The `CSIDriver` object
is normally owned by the driver's add-on — **delete that block** in the forma if
so, or formae will fight the add-on for ownership.

Verify the write reached S3:

```bash
kubectl exec app -- cat /data/demo.txt
aws s3 ls s3://formae-example-s3-gvisor-demo/   # -> demo.txt
```

## Add your own cluster

Append a profile to `platform.pkl`:

```pkl
["my-cluster"] = new {
  k8sVersion = "1.34"
  storageBackend = "dynamic"
  storageClass = "my-sc"
  runtimeHandler = "runsc"                                  // or null
  runtimeNodeSelector = new { ["my/label"] = "sandbox" }    // or omit
}
```

Then `PROFILE=my-cluster formae apply --mode reconcile forma.pkl`. An unknown
`PROFILE` fails fast with the list of valid names.

## PKL package deps

`PklProject` wires all imports to **published packages** on
`hub.platform.engineering` — no local checkout of the plugin repos needed:

- `@formae` → `formae@0.86.1`
- `@k8s` → `k8s@0.1.6` (versioned schema; imports use `@k8s/v1.34/...`)
- `@aws` → `aws@0.1.7`

Run `pkl project resolve` to fetch them, then `PROFILE=<name> pkl eval forma.pkl`.
Schema imports are pinned to `v1.34`; a profile's `k8sVersion` only sets the
target's `kubernetesVersion`. To change the schema minor, bump the
`@k8s/v1.XX/...` import paths.
