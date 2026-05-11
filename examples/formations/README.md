# Formations

Native Forma (PKL) workload bundles for the K8s plugin. Each module
emits a `Listing<formae.Resource>` you spread into your own `forma { }`
block — typed end-to-end, no Helm templating.

Use these as either drop-in deployments or as starting points for your
own workload modules.

## Available formations

| Module | Workload |
|---|---|
| `NginxFormation.pkl` | Minimal nginx deployment + service |
| `CertManagerFormation.pkl` | cert-manager controller, webhook, cainjector |
| `PrometheusFormation.pkl` | Prometheus server |
| `GrafanaFormation.pkl` | Grafana dashboards |
| `PostgreSQLFormation.pkl` | PostgreSQL standalone |
| `RedisFormation.pkl` | Redis standalone |
| `KeycloakFormation.pkl` | Keycloak (DB-backed) |
| `LangfuseFormation.pkl` | Langfuse self-hosted |

## Usage

```pkl
import "@formae-formations/NginxFormation.pkl"

local nginx = new NginxFormation.Nginx {
  namespace = "my-app"
  replicas = 3
}

forma {
  for (r in nginx.resources) { r }
}
```

The `@formae-formations` Pkl alias resolves through the package's
`PklProject` — declare the dep in your own project:

```pkl
dependencies {
  ["formae-formations"] = import("../path/to/examples/formations/PklProject")
}
```
