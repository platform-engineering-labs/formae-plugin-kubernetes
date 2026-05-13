# Provider-Agnostic K8S Auth Design

**Date:** 2026-04-07
**Status:** Draft
**Authors:** Jeroen Soeters

## Problem

The K8S plugin hardcodes AWS EKS authentication: it imports `aws-sdk-go-v2`,
sniffs `*.eks.amazonaws.com` endpoints, and generates STS presigned tokens in
`pkg/config/auth.go`. This makes it impossible to use the resolvable
cross-cloud pattern (`test-resolvable.pkl`) with AKS, GKE, OVH, or OCI managed
Kubernetes offerings.

The naive fix — adding every cloud SDK to the K8S plugin's config package —
would create a tangled dependency graph. We need a clean abstraction.

## Goals

- Support EKS, GKE, AKS, OVH, OCI, and vanilla K8S from a single K8S plugin
  binary
- Explicit auth selection via target config (no URL sniffing)
- Token refresh handled transparently for long-running agent operations
  (sync, discovery)
- Cloud SDK dependencies isolated per-provider
- Cloud plugins only need to expose static cluster metadata (endpoint, CA cert)
  as resolvable properties — no cross-plugin RPC

## Non-Goals

- Cross-plugin callback/RPC mechanism
- Refreshable values in the formae resolvable system
- Relocating `hasLoadBalancer` out of target config (tracked separately)

## Design

### PKL Schema: Target Config

The current flat config is replaced with a structured `auth` field. Auth
strategies are a union of provider-specific types.

```pkl
open class Config {
  hidden defaultNamespace: String?
  hidden hasLoadBalancer: Boolean?
  hidden auth: Auth
}

abstract class Auth {
  /// Discriminator field for Go-side deserialization.
  hidden fixed type: String
}

class KubeconfigAuth extends Auth {
  type = "Kubeconfig"
  hidden context: String?
  hidden kubeconfig: String?
}

class EKSAuth extends Auth {
  type = "EKS"
  hidden endpoint: (String|formae.Resolvable)
  hidden certificateAuthority: (String|formae.Resolvable)
  hidden clusterName: (String|formae.Resolvable)
  hidden region: (String|formae.Resolvable)?
}

class GKEAuth extends Auth {
  type = "GKE"
  hidden endpoint: (String|formae.Resolvable)
  hidden certificateAuthority: (String|formae.Resolvable)
}

class AKSAuth extends Auth {
  type = "AKS"
  hidden endpoint: (String|formae.Resolvable)
  hidden certificateAuthority: (String|formae.Resolvable)
  hidden resourceGroup: (String|formae.Resolvable)?
  hidden clusterName: (String|formae.Resolvable)?
}

class OVHAuth extends Auth {
  type = "OVH"
  hidden endpoint: (String|formae.Resolvable)
  hidden certificateAuthority: (String|formae.Resolvable)
  hidden serviceName: (String|formae.Resolvable)
  hidden clusterId: (String|formae.Resolvable)
}

class OCIAuth extends Auth {
  type = "OCI"
  hidden endpoint: (String|formae.Resolvable)
  hidden certificateAuthority: (String|formae.Resolvable)
  hidden clusterOcid: (String|formae.Resolvable)
  hidden region: (String|formae.Resolvable)?
}
```

**Renamed fields:**
- `namespace` → `defaultNamespace` (clarifies it's a fallback)
- `waitForLoadBalancer` → `hasLoadBalancer` (describes cluster capability,
  not behavior)

**Removed fields from Config top level:**
- `context`, `kubeconfig` → moved into `KubeconfigAuth`
- `endpoint`, `certificateAuthority`, `clusterName` → moved into cloud auth
  types

`endpoint` and `certificateAuthority` live inside each cloud auth type (not
shared on Config) because `KubeconfigAuth` doesn't need them.

### Usage Examples

```pkl
// EKS via resolvables (single-forma cross-cloud)
config = new k8s.Config {
  auth = new k8s.EKSAuth {
    endpoint = eksCluster.res.endpoint
    certificateAuthority = eksCluster.res.certificateAuthorityData
    clusterName = eksCluster.res.name
  }
}

// GKE via resolvables
config = new k8s.Config {
  auth = new k8s.GKEAuth {
    endpoint = gkeCluster.res.endpoint
    certificateAuthority = gkeCluster.res.masterAuth.clusterCaCertificate
  }
}

// AKS via resolvables
config = new k8s.Config {
  auth = new k8s.AKSAuth {
    endpoint = aksCluster.res.fqdn
    certificateAuthority = aksCluster.res.kubeConfig  // needs extraction work
    clusterName = aksCluster.res.name
    resourceGroup = aksCluster.res.resourceGroupName
  }
}

// Local dev with OrbStack
config = new k8s.Config {
  auth = new k8s.KubeconfigAuth {
    context = "orbstack"
  }
  hasLoadBalancer = false
}
```

### Go Architecture: AuthProvider Interface

```go
// pkg/auth/provider.go

// AuthProvider configures a rest.Config with provider-specific authentication.
// Implementations may set bearer tokens, client certificates, or any other
// auth mechanism supported by client-go.
type AuthProvider interface {
    ConfigureTransport(cfg *rest.Config) error
}
```

Single-method interface. Each cloud provider implements it in its own package
with its own SDK dependency. The interface operates on `rest.Config` rather
than returning a raw token string — this avoids hardcoding the bearer-token
path and allows providers to use client certificates, exec-based auth, or
any other mechanism supported by client-go.

For providers that use short-lived bearer tokens (EKS, GKE, AKS, OVH, OCI),
the implementation sets `WrapTransport` on the `rest.Config` to inject fresh
tokens per-request (see Token Refresh section below).

### Shared CachedTokenSource

Cloud providers that generate short-lived bearer tokens share a common
caching layer:

```go
// pkg/auth/cached.go

// TokenSource generates a bearer token with an expiry time.
type TokenSource interface {
    Token(ctx context.Context) (token string, expiry time.Time, err error)
}

// CachedTokenSource wraps a TokenSource with singleflight de-duplication
// and TTL-aware caching. It refreshes when 80% of the token's TTL has
// elapsed.
type CachedTokenSource struct {
    source TokenSource
    mu     sync.Mutex
    token  string
    expiry time.Time
    group  singleflight.Group
}
```

This prevents concurrent discovery/sync requests from each triggering
independent token generation calls, which would cause rate-limit failures
against cloud provider token endpoints (STS, OAuth, Azure AD) under load.

### Package Layout

```
pkg/
  auth/
    provider.go              # AuthProvider interface
    cached.go                # CachedTokenSource (singleflight + TTL cache)
    transport.go             # tokenTransport (WrapTransport helper)
    eks/provider.go          # STS presigned token (aws-sdk-go-v2)
    gke/provider.go          # OAuth2 access token (google cloud SDK)
    aks/provider.go          # Azure AD token (azure SDK)
    ovh/provider.go          # OVH API token (OVH SDK)
    oci/provider.go          # OCI session token (OCI SDK)
  config/
    config.go                # Config struct, auth type dispatching
  transport/
    client.go                # K8S client creation
```

### Auth Dispatching

`Config.ToK8sConfig()` reads the `type` discriminator from the serialized
`Auth` JSON, constructs the appropriate `AuthProvider`, and calls
`ConfigureTransport` on a base `rest.Config`:

- **Cloud auth types** (EKS, GKE, AKS, OVH, OCI): Build a `rest.Config` with
  the endpoint + CA cert, then call `AuthProvider.ConfigureTransport()` which
  sets `WrapTransport` to inject bearer tokens per-request via a
  `CachedTokenSource`.
- **KubeconfigAuth**: Use client-go's native kubeconfig loading (no
  `AuthProvider` involved — client-go handles auth internally).

### Token Refresh via WrapTransport

Instead of setting `BearerToken` once on `rest.Config` (which causes token
expiry on long-running agents), cloud `AuthProvider` implementations set
`WrapTransport` on the `rest.Config` to decorate the HTTP transport with a
`tokenTransport`. This calls through to a `CachedTokenSource` which handles
singleflight de-duplication and TTL-aware caching:

```go
// pkg/auth/transport.go

type tokenTransport struct {
    base   http.RoundTripper
    source *CachedTokenSource
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    token, err := t.source.Token(req.Context())
    if err != nil {
        return nil, err
    }
    req.Header.Set("Authorization", "Bearer "+token)
    return t.base.RoundTrip(req)
}
```

Each cloud provider implements `TokenSource` (returning token + expiry), and
the shared `CachedTokenSource` wraps it to provide:

- **TTL-aware caching**: reuses the token until 80% of its lifetime has elapsed
- **Singleflight**: concurrent requests share a single in-flight token refresh
- **Concurrency safety**: safe under parallel discovery/sync/informer load

This is an idiomatic Go pattern (HTTP `RoundTripper` decorator) used by
client-go itself and cloud SDKs for auth injection.

A typical cloud provider's `AuthProvider` implementation:

```go
// pkg/auth/eks/provider.go

func (p *EKSProvider) ConfigureTransport(cfg *rest.Config) error {
    source := &eksTokenSource{clusterName: p.ClusterName, region: p.Region}
    cached := auth.NewCachedTokenSource(source)
    cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
        return &auth.TokenTransport{Base: rt, Source: cached}
    }
    return nil
}
```

### Formae Core Change: Nested Config Hints

**This is a blocking prerequisite.** The K8S plugin schema change must not
ship until this formae core change is merged and tested. Without it, the
current classifier only inspects top-level keys, meaning a target update
could change nested auth details (e.g., switch endpoint or auth mode)
without being treated as an immutable replacement — silently connecting to
the wrong cluster.

`ClassifyConfigChange` in
`internal/metastructure/target_update/classify_config_change.go` currently only
compares top-level JSON keys. With `Auth` as a nested object, it must support
dot-path hints.

**Change:** `ConfigSchema.Hints` keys support dot-paths (e.g.,
`"Auth.endpoint"`). The classifier either flattens config JSON into dot-paths
or walks nested structures recursively. Fields without a hint still default to
immutable (createOnly), preserving backwards compatibility.

This change lives in the main `formae` repo, not the K8S plugin.

**Merge order:** formae core nested-hint PR → K8S plugin schema change.

PR comment reference: platform-engineering-labs/formae#299 (browdues:
"Nothing to block on, but note that this will only consider top-level keys.
Could bite someday").

### Config Schema Hints

Reported by the K8S plugin to formae for target config change classification:

| Field | Mutable? |
|-------|----------|
| `DefaultNamespace` | Yes |
| `HasLoadBalancer` | Yes |
| `Auth.type` | No (createOnly) |
| `Auth.endpoint` | No (createOnly) |
| `Auth.certificateAuthority` | No (createOnly) |
| `Auth.clusterName` | No (createOnly) |
| `Auth.region` | No (createOnly) |
| All other Auth sub-fields | No (createOnly) |

### What Gets Removed

- `pkg/config/auth.go` — EKS STS code moves to `pkg/auth/eks/`
- `isEKS()` endpoint URL sniffing
- Direct `aws-sdk-go-v2` import from `pkg/config/`
- Top-level `Endpoint`, `CertificateAuthority`, `ClusterName`, `Context`,
  `Kubeconfig` fields on Config struct

### What Gets Updated

- `schema/pkl/k8s.pkl` — new Config + auth type classes
- `pkg/config/config.go` — new Config struct, auth dispatching
- `pkg/transport/client.go` — WrapTransport for cloud auth
- `testdata/*.pkl` — all target configs
- `examples/` — all examples (especially `eks-full-stack/`)
- `conformance test infra` — uses `KubeconfigAuth`
- `go.mod` — new cloud SDK dependencies (GCP, Azure, OVH, OCI)

### Cloud Plugin Prerequisites

For the resolvable cross-cloud pattern to work, each cloud plugin must expose
the required metadata as resolvable properties on their managed K8S resource:

| Plugin | Needs to expose | Current status |
|--------|----------------|----------------|
| AWS (EKS) | endpoint, certificateAuthorityData, name | Already available |
| GCP (GKE) | endpoint, masterAuth.clusterCaCertificate | Already available |
| Azure (AKS) | fqdn, certificateAuthority (from kubeConfig) | fqdn available; CA needs extraction |
| OVH | url, CA (from kubeconfig blob) | url available; CA needs extraction |
| OCI (OKE) | endpoint, CA | Neither exposed yet |

These are independent changes in each cloud plugin repo, not blocking the K8S
plugin work — `KubeconfigAuth` provides a fallback until resolvable properties
are available.

## Auth Mechanism Reference

| Provider | Token type | SDK | Token lifetime |
|----------|-----------|-----|----------------|
| EKS | STS presigned URL | aws-sdk-go-v2 | ~15 min |
| GKE | OAuth2 access token | google cloud Go SDK | ~1 hr |
| AKS | Azure AD token | azure-sdk-for-go | ~1 hr |
| OVH | OVH API token | OVH Go SDK | varies |
| OCI | OCI session token | OCI Go SDK | varies |
| Kubeconfig | n/a (client-go handles) | n/a | n/a |
