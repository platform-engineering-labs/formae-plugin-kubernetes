# LGTM Observability Example

Deploy LGTM (Loki, Grafana, Tempo, Mimir) on a managed Kubernetes cluster on any of {AWS, Azure, GCP, OCI}, then provision Grafana via the grafana plugin (Folder, three DataSources, two Dashboards) in a single forma.

Three plugins compose: the cloud plugin provisions the cluster, the k8s plugin deploys the stack, the grafana plugin configures Grafana over its HTTP API. Target chaining wires it together: the K8s Target's auth is a `$ref` on the cluster endpoint; the Grafana Target's URL is a `$ref` on the Grafana Service's LoadBalancer ingress URL (`http://<host>[:port]`), synthesized by the k8s plugin's Read.

## Prerequisites

- formae ≥ 0.86.0
- `formae-plugin-kubernetes`, `formae-plugin-grafana`, and the cloud plugin for your chosen provider installed
- Cloud credentials in your environment (e.g. `gcloud auth application-default login` for GCP)
- `--provider local` (OrbStack / kind / minikube) works too. Uses the current kubectl context and skips cloud provisioning. Fastest iteration path.

## Environment Variables

| Variable | Value | Description |
|---|---|---|
| `GRAFANA_AUTH` | `admin:admin-change-me` | Matches the password baked into the `lgtm-grafana-admin` Secret. Must be set **before** starting the agent. |
| `FORMAE_PROVIDER` | `gcp` (or another provider) | Optional shortcut for `--provider`. |
| `GCP_PROJECT`, `GCP_APPLY_AS` | (varies) | Required only for the GCP provider. See `clusters/gcp/vars.pkl`. |

## Usage

```bash
formae agent stop
GRAFANA_AUTH=admin:admin-change-me formae agent start

formae apply --mode reconcile --provider gcp --enable-demo-traffic examples/lgtm-observability/main.pkl
```

The apply creates ~55 resources: the cloud bundle (VPC, subnet, router, NAT, cluster, optional IAM grant), the LGTM stack (~48 K8s resources), and the grafana plugin resources (1 Target, 1 Folder, 3 DataSources, 2 Dashboards).

After apply, fetch the Grafana URL from the inventory (the target is labelled per provider so parallel multi-cloud applies don't collide on a single inventory entry):

```bash
formae inventory targets --query 'label:grafana-target-aws' --output-consumer machine --output-schema json | jq -r '.[0].Config.Url."$value"'
```

Open it in a browser. Log in as `admin / admin-change-me`. Navigate to **Dashboards → Formae Dashboards**.

### Wire the agent's OTel exporter so dashboards show data

The dashboards visualize telemetry **from the formae agent itself**. Without enabling the agent's OTel exporter pointed at the cluster's `otel-gateway` Service, the panels stay empty.

1. Uncomment the `oTel` block in `~/.config/formae/formae.conf.pkl`:

   ```pkl
   oTel {
     enabled = true
     serviceName = "formae-agent"
     otlp {
       endpoint = "localhost:4317"
       protocol = "grpc"
       insecure = true
     }
   }
   ```

2. `otel-gateway` is a `ClusterIP` Service. Reach it from the host with `kubectl port-forward` (or expose it as `LoadBalancer` if you'd rather not babysit a forward):

   ```bash
   kubectl port-forward -n observability svc/otel-gateway 4317:4317 &
   ```

3. Restart the agent so the exporter picks up the new config:

   ```bash
   formae agent stop
   GRAFANA_AUTH=admin:admin-change-me formae agent start
   ```

Issue a few formae commands (`formae inventory resources`, etc.) and the panels should start filling.

## What's Deployed

| Resource | Type | Target | Notes |
|---|---|---|---|
| (cluster bundle) | varies | cloud | VPC, subnet, NAT, cluster, IAM |
| `lgtm-grafana-admin` | `K8S::Core::Secret` | k8s | Holds the admin password |
| (LGTM stack) | varies | k8s | ~48 resources: Grafana, Loki, Tempo, Mimir, MinIO, OTel, namespace |
| `grafana-target-{provider}` | `Formae::Target` | - | URL is `$ref` on the Grafana Service's `lbIngressUrl` |
| `formae-dashboards-{provider}` | `Grafana::Core::Folder` | grafana | `uid = "formae-dashboards"` (UIDs are scoped to the per-provider Grafana, so they don't need provider suffixes) |
| `lgtm-{loki,tempo,mimir}-datasource-{provider}` | `Grafana::Core::DataSource` (×3) | grafana | UIDs match dashboards' references |
| `formae-{overview,plugins}-dashboard-{provider}` | `Grafana::Core::Dashboard` (×2) | grafana | Loaded from [formae-grafana-dashboards](https://github.com/platform-engineering-labs/formae-grafana-dashboards) |

## Idempotency

A second apply with no edits should detect zero changes:

```bash
formae apply --mode reconcile --provider gcp examples/lgtm-observability/main.pkl
# Expect "no changes"
```

## Destroy

```bash
formae destroy --on-dependents=cascade --yes --provider gcp examples/lgtm-observability/main.pkl
```

The cascade flag picks up the telemetrygen Deployments that depend on the observability namespace. The teardown order: grafana plugin resources → k8s resources → cloud bundle.

## Limitations / known caveats

- **Anonymous-mode users**: this example forces admin-auth on Grafana so the plugin can authenticate. The legacy `examples/lgtm/main.pkl` retains the original anonymous-admin shape. The two should not be applied to the same stack.
- **First-apply Grafana wait**: the grafana plugin retries on connect failures, so it's safe for the apply to reach the grafana resources before the LB has its public IP. In practice the apply takes 8-10 minutes; LB provisioning lands around minute 5.
- **AWS NLB hostnames**: AWS exposes a `hostname` instead of an `ip` on `status.loadBalancer.ingress`. The synthesized `lbIngressUrl` uses whichever is non-empty, so this works on AWS too.
- **Grafana runs as one replica**: the lgtm bundle pins `replicas = 1` because Grafana's embedded SQLite diverges across pods; an external DB would be required for HA. Restarting the Grafana pod resets its in-pod state; re-apply (or wait for sync) to repopulate.

## How the URL Resolvable works

Pkl can't compose a literal scheme + a Resolvable IP into a single string at user level (`"http://\(svc.res.lbIngressIp)"` would `toString()` the Resolvable). To work around that, the k8s plugin's Read augments each `status.loadBalancer.ingress` entry with a synthesized `url` field built from `http://<host>[:port]` (port omitted for `:80`). The Service schema exposes `lbIngressUrl` (`status.loadBalancer.ingress[0].url`) as a Resolvable so a target's `config.url` can `$ref` it directly.
