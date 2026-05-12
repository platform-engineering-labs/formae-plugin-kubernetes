# LGTM observability stack

Grafana + Loki + Tempo + Mimir + OpenTelemetry collector + MinIO on a managed
Kubernetes cluster. Pick a cloud at apply time with `--provider`. Same forma
file works on AWS, Azure, GCP, OCI, or any kubeconfig-accessible cluster.

## What You Get

**Workload (all providers):**
- Namespace `observability`
- MinIO StatefulSet + 2 Services (S3-compatible object store backing all three data planes)
- Loki: 3 Deployments (write / read / backend) — log aggregation
- Tempo: 3 Deployments (write / read / backend) — distributed tracing
- Mimir: 3 Deployments (write / read / backend) — metrics
- OpenTelemetry Collector (1 Deployment) — telemetry gateway
- Grafana (1 Deployment + Service) — visualization
- Optional: 3 telemetry-generator Deployments (synthetic logs / metrics / traces) — toggled with `--enable-demo-traffic`

Total ~25 pods when fully wired. Fits a 4 CPU / 8Gi cluster.

**Cluster (per provider):** same as the [bookstore example](../bookstore/README.md#what-you-get).

## Prerequisites

Same as [bookstore](../bookstore/README.md#prerequisites). Cloud CLI auth +
matching `formae-plugin-<provider>` + `formae-plugin-k8s`.

## Configuration

Declared CLI flags (auto-generated from `formae.Prop` declarations):

| Flag | Type | Default | Notes |
|------|------|---------|-------|
| `--provider` | string | `$FORMAE_PROVIDER` or `aws` | One of `aws`, `azure`, `gcp`, `oci`, `local`. |
| `--enable-demo-traffic` | bool | `false` | When true, deploys 3 `telemetrygen` workloads that continuously emit synthetic telemetry. Useful for first-time setup; turn off (or omit) once real workloads are wired up. |

Cluster-side knobs live in `clusters/<provider>/vars.pkl` and are not
exposed as CLI flags — see the bookstore [Configuration](../bookstore/README.md#configuration)
for the env-var support matrix.

## Deploy

```bash
# Local cluster, with demo traffic so dashboards have data
formae apply --mode reconcile --yes --watch \
  --provider local --enable-demo-traffic \
  examples/lgtm/main.pkl

# AWS EKS
formae apply --mode reconcile --yes --watch \
  --provider aws --enable-demo-traffic \
  examples/lgtm/main.pkl

# Azure AKS
formae apply --mode reconcile --yes --watch \
  --provider azure --enable-demo-traffic \
  examples/lgtm/main.pkl

# GCP GKE
GCP_PROJECT=my-gcp-project \
  formae apply --mode reconcile --yes --watch \
  --provider gcp --enable-demo-traffic \
  examples/lgtm/main.pkl

# Oracle OKE
OCI_COMPARTMENT_ID=ocid1.compartment.oc1..xxx \
  formae apply --mode reconcile --yes --watch \
  --provider oci --enable-demo-traffic \
  examples/lgtm/main.pkl
```

The data planes (Loki, Tempo, Mimir) take ~1-2 minutes to settle after the
cluster is up. Watch with `formae status command --watch --output-layout detailed`.

## Smoke Test

```bash
# All pods running
kubectl -n observability get pods

# Grafana — port-forward and open
kubectl -n observability port-forward svc/grafana 3000:80
open http://localhost:3000   # anonymous Admin

# In Grafana, the Loki / Tempo / Mimir data sources are pre-configured.
# With --enable-demo-traffic, you should see synthetic data within a minute
# in Explore → Loki / Tempo / Mimir.

# MinIO console (object storage)
kubectl -n observability port-forward svc/minio-console 9001:9001
open http://localhost:9001   # minioadmin / minioadmin-change-me
```

## Tear Down

```bash
formae destroy --yes --provider <p> examples/lgtm/main.pkl
```

## Architecture

```
formae.Stack: k8s-lgtm
│
├── Cloud target + cloud infra + managed cluster (provider-specific)
│
└── K8S target  (EKSAuth / AKSAuth / GKEAuth / OCIAuth / KubeconfigAuth)
    └── Namespace: observability
        ├── MinIO StatefulSet + Services (object store)
        ├── Loki    write / read / backend  ──┐
        ├── Tempo   write / read / backend     │── store data in MinIO
        ├── Mimir   write / read / backend  ──┘
        ├── OpenTelemetry Collector  (gateway)
        ├── Grafana                  (viz)
        └── (optional) 3 telemetrygen Deployments (synthetic traffic)
```

Each data plane (Loki / Tempo / Mimir) runs in **Simple Scalable** mode —
three independently-scaled Deployments per service. Suited for demos and
small production loads; scale up by editing the `apps/lgtm/*.pkl` modules.

## Troubleshooting

| Symptom | Cause / Fix |
|---------|-------------|
| Grafana shows "No data" with `--enable-demo-traffic=false` | Expected. Turn on demo traffic, or wire your own workloads to the OTel gateway: `otel-collector.observability.svc.cluster.local:4317` (gRPC) / `:4318` (HTTP). |
| MinIO Pod `CrashLoopBackOff` | Default credentials are baked in (`minioadmin / minioadmin-change-me`). For real use, edit `apps/lgtm/lgtm.pkl` to source from a real Secret. |
| Loki / Tempo / Mimir Pods stuck `Init` | They wait for MinIO bucket bootstrap. Confirm MinIO Pod is `Running`, then check `kubectl -n observability logs -c init <pod>`. |
| Same per-provider issues as bookstore | See [bookstore Troubleshooting](../bookstore/README.md#troubleshooting). |
