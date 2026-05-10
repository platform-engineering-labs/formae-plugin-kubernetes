# Run nginx Helm chart end-to-end via Formae K8s plugin

Reproduces the local e2e: build formae `0.85.0-dev.7`, install k8s plugin, apply
`bitnami/nginx` Helm chart, verify pods, destroy.

## Prerequisites

- macOS or Linux
- Go 1.23+
- `pkl` 0.30+ (`brew install pkl`)
- `pkl-reader-helm` on PATH (`brew install platform-engineering-labs/tap/pkl-reader-helm` or build from `apple/pkl-pantry`)
- `helm` 3+ with bitnami repo:
  ```bash
  helm repo add bitnami https://charts.bitnami.com/bitnami
  helm repo update
  ```
- `kubectl` pointing at a Kubernetes cluster, e.g. KinD `v1.31.x`:
  ```bash
  kind create cluster --name formae-test --image kindest/node:v1.31.14
  kubectl config use-context kind-formae-test
  kubectl get nodes   # confirm Ready, version v1.31.x
  ```
- Existing orbital tree at `/opt/pel/` (created by the orbital installer). The
  formae agent refuses to start without it.

## 1. Build formae 0.85.0-dev.7 and install to `/opt/pel/bin`

```bash
git clone git@github.com:platform-engineering-labs/formae.git
cd formae
git checkout 0.85.0-dev.7

# Build the binary (writes ./formae in repo root)
make build

# Install over the orbital-managed binary
sudo cp formae /opt/pel/bin/formae
/opt/pel/bin/formae --version   # expect 0.85.0-dev.7

# Start the agent
/opt/pel/bin/formae agent start &
```

Stop with `/opt/pel/bin/formae agent stop` when finished.

## 2. Build and install the K8s plugin

```bash
git clone git@github.com:platform-engineering-labs/formae-plugin-k8s.git
cd formae-plugin-k8s

make install
```

This generates `schema/pkl/generated/` (every supported K8s minor), builds the
plugin binary, and installs into `~/.pel/formae/plugins/k8s/v<version>/` with
the binary, manifest, and the full multi-version PKL schema. Formae's runtime
picks the right schema per target via `kubernetesVersion`. Verify:

```bash
ls ~/.pel/formae/plugins/k8s/
```

## 3. Apply the nginx Helm chart

The repo ships `examples/helm/nginx-v1.31.pkl`, pinned to `@k8s/v1.31` and
`@formae-helm/v1.31`. From the plugin repo root:

```bash
# One-time PKL dependency resolve
pkl project resolve examples/helm/

# Apply
/opt/pel/bin/formae apply examples/helm/nginx-v1.31.pkl --mode reconcile --yes --watch
```

Expected: 1 stack, 1 target, 5 resources (Namespace, Deployment, Service,
ServiceAccount, PodDisruptionBudget) — Status `Success`.

Verify in-cluster:

```bash
kubectl -n formae-helm-test get all
# pod/my-nginx-...   1/1 Running
# pod/my-nginx-...   1/1 Running
# service/my-nginx   ClusterIP   ...
# deployment.apps/my-nginx   2/2
```

## 4. Destroy

```bash
/opt/pel/bin/formae destroy examples/helm/nginx-v1.31.pkl --yes --watch
kubectl get ns formae-helm-test   # NotFound
```

## Troubleshooting

- **`agent is not running`** — start with `/opt/pel/bin/formae agent start`.
- **`orbital tree not initialized`** — the dev binary in `~/git/pel/formae/`
  rejects start. Use `/opt/pel/bin/formae` (the orbital-installed copy).
- **`pkl-reader-helm: not found`** — install it on PATH; the plugin renders
  the chart at PKL eval time via this external resource reader.
- **Cluster on a different K8s minor** — switch the imports in the forma file
  (`@k8s/v1.X/...` and `@formae-helm/v1.X/...`) and `kubernetesVersion = "1.X"`
  in the target `Config`. The installed plugin already ships every supported
  minor, so no re-install needed.
