# Examples

End-to-end examples that deploy real workloads to a Kubernetes cluster using Formae and the K8S plugin. The examples are organized by approach:

```
examples/
├── webapp.pkl              # Hand-written forma (raw K8S types)
├── webapp-v2.pkl           # Rolling update of the above
├── nginx-ingress.pkl       # Nginx ingress controller
├── charts/                 # Native PKL charts (no Helm)
│   ├── nginx.pkl           # NginxChart example
│   └── langfuse.pkl        # LangfuseChart example
└── helm/                   # Helm bridge examples (requires pkl-reader-helm)
    ├── nginx.pkl           # HelmChart: bitnami/nginx
    ├── nginx-generator.pkl # Generator: complete forma in one object
    └── nginx-static.pkl    # StaticGenerator: generate self-contained PKL
```

## Prerequisites

- A running Kubernetes cluster (OrbStack, minikube, kind, etc.)
- `formae` binary installed and on your `PATH`
- The K8S plugin built and installed (`make install` from the plugin root)

Additional for `helm/` examples:
- [pkl-reader-helm](https://github.com/apple/pkl-readers/tree/main/helm) on `PATH`
- Helm v3+ with repos configured (`helm repo add bitnami https://charts.bitnami.com/bitnami`)

## Quick Start

```bash
# Deploy the bookstore app (hand-written, no extra deps)
formae apply examples/webapp.pkl

# Or deploy nginx via a native chart (no Helm needed)
cd examples/charts
formae apply nginx.pkl

# Or deploy nginx via Helm bridge (needs pkl-reader-helm)
cd examples/helm
formae apply nginx.pkl
```

---

## Hand-Written Examples

These use `formae-plugin-k8s` types directly — no charts, no Helm. Good for learning the type system and seeing exactly what Formae manages.

### Bookstore Web Application

A full-stack web app with an nginx frontend and a Node.js backend API.

**Files:** `webapp.pkl`, `webapp-v2.pkl`

| Resource | Type | Description |
|----------|------|-------------|
| `bookstore-ns` | Namespace | Dedicated namespace |
| `bookstore-frontend-config` | ConfigMap | Nginx config + HTML |
| `bookstore-backend-config` | ConfigMap | Backend env vars |
| `bookstore-db-credentials` | Secret | Database credentials |
| `bookstore-backend-sa` | ServiceAccount | Backend pod identity |
| `bookstore-frontend` | Deployment | Nginx (2 replicas) |
| `bookstore-backend` | Deployment | Node.js API (3 replicas) |
| `bookstore-frontend-svc` | Service | Frontend ClusterIP |
| `bookstore-backend-svc` | Service | Backend ClusterIP |
| `bookstore-ingress` | Ingress | Routes `/` and `/api` |

#### Deploy v1

```bash
formae apply examples/webapp.pkl
```

#### Access via port-forward

```bash
kubectl -n bookstore port-forward svc/bookstore-frontend 8080:80
```

Then open http://localhost:8080. The frontend fetches `/api` and displays the JSON response.

**v1 API response:**

```json
{"status":"ok","path":"/api","ts":"2026-02-11T16:35:00.000Z"}
```

#### Rolling update to v2

`webapp-v2.pkl` changes the frontend HTML and backend API response. Formae detects the property diffs and patches only the changed resources (the frontend ConfigMap and backend Deployment):

```bash
formae apply examples/webapp-v2.pkl
```

Re-run the port-forward (kill the old one first with Ctrl+C):

```bash
kubectl -n bookstore port-forward svc/bookstore-frontend 8080:80
```

The page now shows "Bookstore v2" with a blue theme, and the API includes version info:

**v2 API response:**

```json
{"version":"2.0","status":"ok","path":"/api","features":["catalog-search","recommendations"],"ts":"2026-02-11T16:37:00.000Z"}
```

#### Rollback to v1

```bash
formae apply examples/webapp.pkl
```

#### Teardown

```bash
formae destroy examples/webapp.pkl
```

### Nginx Ingress Controller

Deploys the nginx ingress controller so that `Ingress` resources are served. This is cluster infrastructure -- deploy once, then use `webapp.pkl`.

**File:** `nginx-ingress.pkl`

| Resource | Type | Description |
|----------|------|-------------|
| `ingress-nginx-ns` | Namespace | Controller namespace |
| `ingress-nginx-sa` | ServiceAccount | Controller identity |
| `ingress-nginx-role` | ClusterRole | RBAC permissions |
| `ingress-nginx-binding` | ClusterRoleBinding | Binds role to SA |
| `ingress-nginx-config` | ConfigMap | Nginx controller config |
| `ingress-nginx-controller` | Deployment | The controller pod |
| `ingress-nginx-svc` | Service | NodePort on 80/443 |
| `ingress-nginx-class` | IngressClass | Registers "nginx" class |

#### Deploy

```bash
formae apply examples/nginx-ingress.pkl
```

#### Verify

```bash
kubectl -n ingress-nginx get pods       # controller should be Running
kubectl get ingressclass                # "nginx" should appear
```

#### Access webapp via Ingress (instead of port-forward)

Add to `/etc/hosts`:

```
127.0.0.1 bookstore.example.com
```

Then browse:
- http://bookstore.example.com/ -- frontend
- http://bookstore.example.com/api -- backend API

#### Teardown

```bash
formae destroy examples/nginx-ingress.pkl
```

---

## Native Chart Examples (`charts/`)

These use reusable PKL chart classes from the [`charts/`](../charts/) package. Pure PKL, no Helm dependency. The chart handles resource wiring — you just set configuration values.

> **Dependency:** `formae-charts` (local `../../charts/PklProject`)

### Nginx

Deploys nginx with a Deployment, Service, ServiceAccount, and Namespace using `NginxChart`.

**File:** `charts/nginx.pkl`

```bash
cd examples/charts
pkl eval -f pcf nginx.pkl           # preview the generated forma
formae apply nginx.pkl              # deploy to cluster
formae destroy nginx.pkl            # teardown
```

**What it creates:**

| Resource | Type | Description |
|----------|------|-------------|
| `my-nginx-namespace` | Namespace | `formae-test` |
| `my-nginx-serviceaccount` | ServiceAccount | Dedicated SA |
| `my-nginx-service` | Service | ClusterIP on port 80 |
| `my-nginx-deployment` | Deployment | nginx:1.27 (2 replicas) |

Configuration in the example:

```pkl
local nginx = new NginxChart.Nginx {
  name = "my-nginx"
  namespace = "formae-test"
  replicas = 2
  image = "nginx:1.27"
  serviceType = "ClusterIP"
  cpuRequest = "100m"
  cpuLimit = "200m"
  memoryRequest = "128Mi"
  memoryLimit = "256Mi"
  createNamespace = true
  createServiceAccount = true
}
```

### Langfuse

Deploys the [Langfuse](https://langfuse.com/) LLM observability platform with web server, worker, Secret, ServiceAccount, Namespace, and Ingress.

**File:** `charts/langfuse.pkl`

**Prerequisites:** PostgreSQL and Redis must be deployed separately (or use managed services).

```bash
cd examples/charts
pkl eval -f pcf langfuse.pkl        # preview
formae apply langfuse.pkl           # deploy
formae destroy langfuse.pkl         # teardown
```

**What it creates:**

| Resource | Type | Description |
|----------|------|-------------|
| `langfuse-namespace` | Namespace | `langfuse` |
| `langfuse-serviceaccount` | ServiceAccount | Shared SA |
| `langfuse-secret` | Secret | Auth keys + DB password |
| `langfuse-web-service` | Service | Web ClusterIP on port 3000 |
| `langfuse-web-deployment` | Deployment | Web server (2 replicas) |
| `langfuse-worker-deployment` | Deployment | Worker (1 replica) |
| `langfuse-ingress` | Ingress | TLS on langfuse.example.com |

---

## Helm Bridge Examples (`helm/`)

These use the [`helm/`](../helm/) package to render real Helm charts into typed Formae resources. Requires `pkl-reader-helm` at evaluation time.

> **Dependency:** `formae-helm` (local `../../helm/PklProject`)

See the [helm README](../helm/README.md) for full documentation on HelmChart, Generator, and StaticGenerator.

### Nginx via HelmChart

Low-level usage — renders `bitnami/nginx` and spreads resources into a hand-built forma with Stack, Target, and Namespace.

**File:** `helm/nginx.pkl`

```bash
cd examples/helm
pkl eval -f pcf nginx.pkl           # preview (needs pkl-reader-helm + helm)
formae apply nginx.pkl              # deploy
```

### Nginx via Generator

One-object convenience — `Generator.HelmRelease` produces Stack, Target, optional Namespace, and all chart resources in a single `formaEntries` property.

**File:** `helm/nginx-generator.pkl`

```bash
cd examples/helm
pkl eval -f pcf nginx-generator.pkl > nginx-forma.pcf
formae apply --mode reconcile nginx-forma.pcf
```

### Nginx via StaticGenerator

Generates self-contained PKL source code that does **not** need `pkl-reader-helm` at deploy time. Ideal for CI/CD.

**File:** `helm/nginx-static.pkl`

```bash
cd examples/helm

# Step 1: Generate static PKL (needs pkl-reader-helm)
pkl eval nginx-static.pkl > nginx-forma.pkl

# Step 2: Deploy (no pkl-reader-helm needed)
formae apply --mode reconcile nginx-forma.pkl
```

---

## Comparison: Which Approach to Use?

| Approach | Complexity | Helm needed? | Best for |
|----------|-----------|-------------|----------|
| **Hand-written** (`webapp.pkl`) | Full control | No | Learning, custom apps, exact resource specs |
| **Native charts** (`charts/nginx.pkl`) | Configuration only | No | Reusable app deployments, team templates |
| **Helm bridge** (`helm/nginx.pkl`) | Configuration only | Yes (runtime) | Using existing Helm charts from registries |
| **Static generation** (`helm/nginx-static.pkl`) | Configuration only | Yes (build time) | CI/CD, air-gapped environments |
