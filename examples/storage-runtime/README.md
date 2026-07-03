# Storage + sandboxed runtime

Give a Pod some storage and (optionally) run it under a sandbox runtime like
**gVisor**. One reusable workload, one file per environment — the same layout as
the other multi-target examples (`bookstore/`, `clusters/`, ...).

| File | Environment | Storage | Runtime |
|------|-------------|---------|---------|
| [`local.pkl`](./local.pkl) | OrbStack / kind / k3s (kubeconfig) | `local-path` dynamic PVC | default |
| [`minikube.pkl`](./minikube.pkl) | minikube + gVisor addon | `standard` dynamic PVC | gVisor (`runsc`) |
| [`gke.pkl`](./gke.pkl) | GKE Sandbox | `standard-rwo` dynamic PVC | gVisor (`runsc`) |
| [`eks.pkl`](./eks.pkl) | EKS + Mountpoint S3 CSI | S3 bucket + CSI + static PV | gVisor (`runsc`) |

Each is a thin forma: it fills in a `workload.Config`, then spreads the shared
[`workload.pkl`](./workload.pkl) module. The Pod always mounts the storage at
`/data` and writes `demo.txt` to prove it works.

```bash
cd examples/storage-runtime
formae apply --mode reconcile local.pkl      # runs as-is on any local cluster
kubectl exec app -- cat /data/demo.txt       # -> "hello from formae at <date>"
formae destroy --yes local.pkl
```

## How it's structured

`workload.pkl` is the reusable part — a `Config` class plus two functions:

- `targets(cfg)` → the K8s target (always) and an AWS target (S3 backend only)
- `resources(cfg)` → the storage + runtime + Pod resources, emitted
  conditionally with `when (...)` so each environment gets only what it needs

The environment files supply the `Config` and assemble the forma:

```pkl
local cfg = new workload.Config {
  k8sVersion = "1.34"
  storageBackend = "dynamic"          // or "s3"
  storageClass = "standard"
  runtimeHandler = "runsc"            // or null for the default runtime
  runtimeNodeSelector = new { ["sandbox.gke.io/runtime"] = "gvisor" }
}
forma {
  stack
  ...workload.targets(cfg)
  ...workload.resources(cfg)
}
```

Add a cluster of your own by copying one env file and editing the `Config`.

## The gVisor bit

gVisor = the `runsc` OCI runtime. A `RuntimeClass` only *references* a handler
the node's containerd already registers — it does **not** install gVisor. So the
sandbox environments need `runsc` present on a node:

- **Local:** minikube with the gVisor addon — the only turnkey local option
  (kind/OrbStack ship no `runsc`, and no `kindest/node` image bundles gVisor):
  ```bash
  minikube start --container-runtime=containerd
  minikube addons enable gvisor        # installs the runsc handler on the node
  formae apply --mode reconcile minikube.pkl
  ```
  (Check the addon supports your k8s version; it has lagged newer releases.)
- **Cloud:** GKE Sandbox (`gke.pkl`) or a node pool with gVisor installed
  yourself.

If a handler isn't on the node, the Pod stays `ContainerCreating` with
`FailedCreatePodSandBox: RuntimeHandler "runsc" not supported` — that's the
cluster missing gVisor, not the forma.

## The S3 bit (`eks.pkl`)

Mountpoint for Amazon S3 is **static-only** — no StorageClass, no dynamic
provisioning. `workload.pkl` emits a `PersistentVolume` with a `csi:` source
pointing at the bucket, and a PVC with `storageClassName = ""` that pins it via
`volumeName`. Capacity numbers are required by the API but ignored by S3.

Needs, on the cluster: the AWS plugin + creds, the
[Mountpoint S3 CSI driver](https://github.com/awslabs/mountpoint-s3-csi-driver)
with pod/node IAM for the bucket, and gVisor on a node. The `CSIDriver` object
is normally owned by the driver's add-on — **delete that block** in
`workload.pkl` if so, or formae will fight the add-on for ownership.

```bash
kubectl exec app -- cat /data/demo.txt
aws s3 ls s3://formae-example-s3-gvisor-demo/   # -> demo.txt
```

## PKL package deps

`PklProject` wires all imports to **published packages** on
`hub.platform.engineering` — no local checkout of the plugin repos needed:

- `@formae` → `formae@0.86.1`
- `@k8s` → `k8s@0.1.6` (versioned schema; imports use `@k8s/v1.34/...`)
- `@aws` → `aws@0.1.7`

Run `pkl project resolve` to fetch them, then `pkl eval local.pkl`. Schema
imports are pinned to `v1.34`; a `Config`'s `k8sVersion` only sets the target's
`kubernetesVersion`. To change the schema minor, bump the `@k8s/v1.XX/...`
import paths in `workload.pkl`.
