# LGTM Observability Stack — Quick Start

Runs a prod-like **Grafana + Loki + Tempo + Mimir + OTel + MinIO** stack on
any K8S target. Each data-plane service (Loki, Tempo, Mimir) deploys as 3
independently scaled Deployments (`read` / `write` / `backend`).

## Prerequisites

- `formae` CLI + a running formae agent (`formae agent start`)
- `kubectl` configured with a working context (OrbStack, minikube, EKS, GKE…)
- Cluster with ~4 CPU / 8Gi free — the stack creates **45 K8S resources** and
  runs 25+ pods once images are pulled
- For OrbStack: `orb config set k8s.enable true` (first time only)

## Deploy

Run **from the repo root** (`formae-plugin-k8s/`):

```bash
formae apply --mode reconcile --yes examples/apps/lgtm/example-orbstack.pkl
```

Expected: ~3–5 min on OrbStack (image pulls dominate). You'll see all
resources reach `Success`. Loki/Tempo/Mimir pods may restart 1–2× during
initial memberlist ring formation — normal.

## Verify (takes ~30s each)

**1. All pods Running**

```bash
kubectl -n observability get pods
```

Every pod should be `Running` after ~2 min of stabilization. If anything
stays in `CrashLoopBackOff`, tail its logs:

```bash
kubectl -n observability logs <pod> --tail=50
```

**2. MinIO buckets auto-created**

```bash
kubectl -n observability exec sts/minio -- sh -c \
  'mc alias set local http://localhost:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" && mc ls local'
```

Expected: three lines — `loki-data`, `mimir-blocks`, `tempo-traces`.

Or browse the MinIO console:

```bash
kubectl -n observability port-forward svc/minio-console 9001
open http://localhost:9001   # minioadmin / minioadmin-change-me
```

**3. Grafana reachable**

```bash
kubectl -n observability port-forward svc/grafana 3000:80
open http://localhost:3000   # anonymous Admin — no login needed
```

(Or `kubectl -n observability get svc grafana` for the OrbStack LB IP.)

**4. End-to-end telemetry in Grafana**

Telemetry is produced continuously by three `telemetrygen` Deployments.
In the Grafana UI → **Explore**:

| Datasource | Query | Expect |
|---|---|---|
| Loki | `{service_name="telemetrygen"}` | Log lines streaming in |
| Tempo | service name `telemetrygen` | Trace spans with 1s cadence |
| Mimir | `{__name__=~"gen_.+"}` | Time-series data |

Or open **Dashboards → LGTM Demo** — three panels, one per signal, should
all be populated.

## Teardown

```bash
formae destroy --yes examples/apps/lgtm/example-orbstack.pkl
```

Removes all 45 resources. The namespace + PVC release promptly on OrbStack.

## Troubleshooting

- **Stuck at "apply started" with 0 Progress** — usually a formae agent
  issue. Check with `ps aux | grep "formae agent"`; restart if needed:
  `formae agent stop && formae agent start`.
- **Pods Pending on image pull** — first apply pulls ~3Gi of images. Give
  it 5+ min on slow networks.
- **Grafana panels empty** — wait 1–2 min after pods go Ready, then refresh.
  `telemetrygen` only starts producing once gateway Service is reachable.
- **Loki/Tempo/Mimir pods crashlooping with memberlist errors** — usually
  self-heals on restart. If persistent, `kubectl -n observability delete pod`
  to force a re-roll.

## Toggling demo traffic

`telemetrygen` is on by default so the dashboard has data on first load. When
pointing the stack at real workloads, open `example-orbstack.pkl` and set:

```pkl
local enableDemoTraffic = false
```

Re-apply — the three telemetrygen Deployments are removed and dashboards will
only show genuine traffic from your own apps.

## Customizing

`lgtm.pkl` composes seven sub-modules, each exporting its own
`allResources(k8sTarget, appNs, …)`:

| File | Scales |
|---|---|
| `loki.pkl` | `tier()` helper, called 3× with replicas 2/2/1 |
| `tempo.pkl` | same, OTLP exposed on write tier |
| `mimir.pkl` | same, native `-target=read/write/backend` |
| `otel.pkl` | gateway (2 replicas) + agent DaemonSet |
| `grafana.pkl` | 2 replicas, stateless |
| `minio.pkl` | 1-replica StatefulSet, 20Gi PVC |
| `demo.pkl` | 3 telemetrygen producers |

Adjust replica counts at the `allResources` call sites in each sub-module,
or rewrite `example-orbstack.pkl` to point at a different target
(EKS full-stack, GKE, etc.) the same way `vanilla.pkl` does for bookstore.
