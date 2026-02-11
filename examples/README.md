# Examples

End-to-end examples that deploy real workloads to a Kubernetes cluster using Formae and the K8S plugin.

## Prerequisites

- A running Kubernetes cluster (OrbStack, minikube, kind, etc.)
- `formae` binary installed and on your `PATH`
- The K8S plugin built and installed (`make install` from the plugin root)

## Bookstore Web Application

A full-stack web app with an nginx frontend and a Node.js backend API.

### Resources

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

### Deploy v1

```bash
formae apply examples/webapp.pkl
```

### Access via port-forward

```bash
kubectl -n bookstore port-forward svc/bookstore-frontend 8080:80
```

Then open http://localhost:8080. The frontend fetches `/api` and displays the JSON response.

**v1 API response:**

```json
{"status":"ok","path":"/api","ts":"2026-02-11T16:35:00.000Z"}
```

### Rolling update to v2

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

### Rollback to v1

```bash
formae apply examples/webapp.pkl
```

### Teardown

```bash
formae destroy examples/webapp.pkl
```

## Nginx Ingress Controller (optional)

Deploys the nginx ingress controller so that `Ingress` resources are served. This is cluster infrastructure -- deploy once, then use `webapp.pkl`.

### Resources

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

### Deploy

```bash
formae apply examples/nginx-ingress.pkl
```

### Verify

```bash
kubectl -n ingress-nginx get pods       # controller should be Running
kubectl get ingressclass                # "nginx" should appear
```

### Access webapp via Ingress (instead of port-forward)

Add to `/etc/hosts`:

```
127.0.0.1 bookstore.example.com
```

Then browse:
- http://bookstore.example.com/ -- frontend
- http://bookstore.example.com/api -- backend API

### Teardown

```bash
formae destroy examples/nginx-ingress.pkl
```
