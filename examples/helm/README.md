# Helm

Example forma files that render Helm charts through `formae-helm` and produce
typed K8s resources you can `formae apply` like any other workload. The bridge
lets you keep Helm charts as the source of truth for upstream operators while
managing the rendered output as plain Formae resources.

## Examples

| File                      | Chart                                    | K8s version pin |
|---------------------------|------------------------------------------|-----------------|
| `nginx.pkl`               | bitnami/nginx 22.4.7                     | (version-agnostic) |
| `nginx-v1.31.pkl`         | bitnami/nginx, fixtures for K8s v1.31    | 1.31 |
| `nginx-v1.34.pkl`         | bitnami/nginx, fixtures for K8s v1.34    | 1.34 |
| `postgresql-v1.31.pkl`    | bitnami/postgresql                       | 1.31 |
| `memcached-v1.31.pkl`     | bitnami/memcached                        | 1.31 |

The `-v<minor>` variants exist because Helm charts can emit resources whose
shape depends on the API server's minor version (e.g. `policy/v1` vs
`policy/v1beta1`). Pick the variant that matches your cluster.

## Prerequisites

- `pkl-reader-helm` binary on `PATH` (see [`helm/README.md`](../../helm/README.md))
- A Helm repo configured:
  ```bash
  helm repo add bitnami https://charts.bitnami.com/bitnami
  helm repo update
  ```

## Use

```bash
pkl project resolve examples/helm/
formae apply --mode reconcile --yes --watch examples/helm/nginx.pkl
```
