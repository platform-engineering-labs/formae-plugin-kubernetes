# Example: S3 bucket → Mountpoint CSI → gVisor-sandboxed Pod

Illustrative, cross-plugin forma. Shows how an **AWS S3 bucket**, the
**Mountpoint for Amazon S3 CSI driver**, and a **gVisor RuntimeClass** compose
so a workload gets S3-backed storage while running in a sandbox.

> Teaching artifact, not a conformance test. It spans two providers (AWS + K8s)
> and depends on cluster add-ons that must already be installed. Read it, adapt
> the names/region/node labels, then apply.

## The wiring

```
AWS::S3::Bucket  (demo-bucket)                         data backend
        │  bucketName
        ▼
K8S::Storage::CSIDriver  (s3.csi.aws.com)              driver registration*
K8S::Core::PersistentVolume  (s3-pv, csi → bucket)     STATIC volume
        │  volumeName
        ▼
K8S::Core::PersistentVolumeClaim  (s3-pvc)             claim
        │  claimName
        ▼
K8S::Core::Pod  (s3-writer)  ── runtimeClassName ──►  K8S::Node::RuntimeClass (gvisor, runsc)
   mounts s3-pvc at /data, writes demo.txt into the bucket
```

`*` The `CSIDriver` object is normally installed by the
`aws-mountpoint-s3-csi-driver` Helm chart / EKS add-on. It is included in the
forma only to show the resource type — **delete that block** if the add-on
already owns it (formae would otherwise fight the add-on for ownership).

## Two things that trip people up

- **Mountpoint S3 is static-only.** There is no StorageClass and no dynamic
  provisioning. You create a `PersistentVolume` with a `csi:` source pointing at
  the bucket, and a PVC with `storageClassName = ""` that pins it via
  `volumeName`. Capacity numbers are required by the API but ignored by S3.
- **RuntimeClass and storage are orthogonal.** The RuntimeClass only sets the
  Pod's runtime (`handler = "runsc"` → gVisor) and pins scheduling to
  gVisor-capable nodes. It does not touch the volume. They meet only inside the
  Pod spec.

## Prerequisites (for a real apply)

1. **AWS plugin** installed (`AWS::S3::Bucket`) and AWS creds for the target region.
2. **Mountpoint S3 CSI driver** on the cluster, with pod/node IAM (IRSA or node
   role) allowing `s3:*` on the bucket. See
   <https://github.com/awslabs/mountpoint-s3-csi-driver>.
3. **gVisor** on at least one node pool, labelled to match the RuntimeClass
   `scheduling.nodeSelector` (the example uses the GKE Sandbox label
   `sandbox.gke.io/runtime=gvisor`; change it for self-managed gVisor).
4. A `default` namespace (or edit the `namespace` fields).

## Apply

```bash
cd examples/s3-mountpoint-gvisor
formae apply --mode reconcile --simulate forma.pkl   # preview, no changes
formae apply --mode reconcile forma.pkl              # execute
```

Then verify the sandbox wrote through to S3:

```bash
kubectl logs s3-writer            # -> "hello from gVisor at <date>"
aws s3 ls s3://formae-example-s3-gvisor-demo/   # -> demo.txt
```

## Run it locally (no cloud)

`forma.local.pkl` is a runnable variant for a vanilla single-node cluster
(orbstack / kind / k3s). It swaps the two prereqs a local cluster lacks:

- S3 + Mountpoint CSI → the cluster's built-in `local-path` StorageClass
- gVisor RuntimeClass → a managed RuntimeClass object that is created but not
  attached to the Pod (a stock node has no `runsc`/`crun`/`runc` handler, so
  `runtimeClassName` is left commented — see the note in the file)

```bash
cd examples/s3-mountpoint-gvisor
formae apply --mode reconcile forma.local.pkl
kubectl exec demo-writer -- cat /data/demo.txt   # -> "hello from formae at <date>"
formae destroy forma.local.pkl                   # clean up
```

To run the gVisor half locally too, use minikube with the gVisor addon
(`minikube addons enable gvisor`), then set `handler = "runsc"` and uncomment
`runtimeClassName` in `forma.local.pkl`.

## PKL package deps

`PklProject` here wires the imports: `@formae` (published package), `@k8s`
(local `../../schema/pkl-main`), and `@aws` (local
`../../../formae-plugin-aws/schema/pkl`). Run `pkl project resolve` if the AWS
plugin path differs. `forma.local.pkl` is K8s-only and needs no AWS dep.
