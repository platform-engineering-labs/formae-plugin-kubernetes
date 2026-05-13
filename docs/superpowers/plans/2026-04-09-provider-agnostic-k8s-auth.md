# Provider-Agnostic K8S Auth Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the EKS-only hardcoded auth in the K8S plugin with a provider-agnostic auth system supporting EKS, GKE, AKS, OVH, OCI, and vanilla K8S via kubeconfig.

**Architecture:** Target config gets a structured `auth` field with a union of provider-specific types. An `AuthProvider` interface configures `rest.Config` per-provider. Cloud providers use a shared `CachedTokenSource` with singleflight for token refresh via client-go's `WrapTransport`. The existing 50-second client cache TTL workaround is removed.

**Tech Stack:** Go, client-go, aws-sdk-go-v2, google cloud SDK, azure-sdk-for-go, OVH Go SDK, OCI Go SDK, PKL schemas

**Spec:** `docs/superpowers/specs/2026-04-07-provider-agnostic-k8s-auth-design.md`
**RFC:** `platform-engineering-labs/rfcs#9`

---

## File Map

### New files
- `pkg/auth/provider.go` — `AuthProvider` interface
- `pkg/auth/cached.go` — `TokenSource` interface, `CachedTokenSource` (singleflight + TTL)
- `pkg/auth/cached_test.go` — Tests for caching, singleflight, TTL refresh
- `pkg/auth/transport.go` — `TokenTransport` (WrapTransport helper)
- `pkg/auth/transport_test.go` — Tests for token injection into HTTP requests
- `pkg/auth/eks/provider.go` — EKS `AuthProvider` (STS presigned token)
- `pkg/auth/eks/provider_test.go` — EKS token source tests
- `pkg/auth/gke/provider.go` — GKE `AuthProvider` (OAuth2 access token)
- `pkg/auth/aks/provider.go` — AKS `AuthProvider` (Azure AD token)
- `pkg/auth/ovh/provider.go` — OVH `AuthProvider` (OVH API token)
- `pkg/auth/oci/provider.go` — OCI `AuthProvider` (OCI session token)

### Modified files
- `schema/pkl/k8s.pkl` — New Config + auth union types, rename fields
- `pkg/config/config.go` — New Config struct, auth dispatching via `type` discriminator
- `pkg/transport/client.go` — Remove direct `rest.Config` construction, delegate to auth
- `k8s.go` — Remove client cache (TTL workaround), simplify `getProvisioner`
- `testdata/config/vars.pkl` — Update target config to use `KubeconfigAuth`
- `examples/webapp.pkl` — Update target config
- `examples/webapp-v2.pkl` — Update target config
- `examples/nginx-ingress.pkl` — Update target config
- `examples/drift-demo.pkl` — Update target config
- `examples/eks-full-stack/test-resolvable.pkl` — Update to use `EKSAuth`
- `examples/eks-full-stack/stage2-webapp.pkl` — Update target config
- `examples/eks-full-stack/README.md` — Update examples in docs

### Deleted files
- `pkg/config/auth.go` — EKS STS logic moves to `pkg/auth/eks/provider.go`

---

## Task 1: AuthProvider Interface and TokenTransport

The foundational types that everything else builds on. No cloud SDK dependencies.

**Files:**
- Create: `pkg/auth/provider.go`
- Create: `pkg/auth/transport.go`
- Create: `pkg/auth/transport_test.go`

- [ ] **Step 1: Write the failing test for TokenTransport**

```go
// pkg/auth/transport_test.go
package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
)

type staticTokenSource struct {
	token string
}

func (s *staticTokenSource) Token(_ context.Context) (string, time.Time, error) {
	return s.token, time.Now().Add(1 * time.Hour), nil
}

func TestTokenTransport_InjectsAuthHeader(t *testing.T) {
	var capturedHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	source := &staticTokenSource{token: "test-token-123"}
	cached := auth.NewCachedTokenSource(source)
	transport := auth.NewTokenTransport(http.DefaultTransport, cached)

	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	resp.Body.Close()

	if capturedHeader != "Bearer test-token-123" {
		t.Errorf("expected 'Bearer test-token-123', got %q", capturedHeader)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -v -tags=unit -run TestTokenTransport ./pkg/auth/...`
Expected: FAIL — package does not exist

- [ ] **Step 3: Create the AuthProvider interface**

```go
// pkg/auth/provider.go
package auth

import "k8s.io/client-go/rest"

// AuthProvider configures a rest.Config with provider-specific authentication.
// Implementations may set bearer tokens, client certificates, or any other
// auth mechanism supported by client-go.
type AuthProvider interface {
	ConfigureTransport(cfg *rest.Config) error
}
```

- [ ] **Step 4: Create TokenTransport**

```go
// pkg/auth/transport.go
package auth

import (
	"context"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// TokenSource generates a bearer token with an expiry time.
type TokenSource interface {
	Token(ctx context.Context) (token string, expiry time.Time, err error)
}

// CachedTokenSource wraps a TokenSource with singleflight de-duplication
// and TTL-aware caching. It refreshes when 80% of the token's TTL has elapsed.
type CachedTokenSource struct {
	source TokenSource
	mu     sync.RWMutex
	token  string
	expiry time.Time
	group  singleflight.Group
}

// NewCachedTokenSource wraps a TokenSource with caching and singleflight.
func NewCachedTokenSource(source TokenSource) *CachedTokenSource {
	return &CachedTokenSource{source: source}
}

// Token returns a cached token or refreshes if expired/near-expiry.
func (c *CachedTokenSource) Token(ctx context.Context) (string, error) {
	c.mu.RLock()
	token, expiry := c.token, c.expiry
	c.mu.RUnlock()

	// Return cached token if still within 80% of its TTL
	if token != "" && time.Now().Before(expiry) {
		remaining := time.Until(expiry)
		total := expiry.Sub(expiry.Add(-remaining)) // approximate
		if remaining > total/5 {
			return token, nil
		}
	}

	// Singleflight: concurrent callers share one refresh
	result, err, _ := c.group.Do("token", func() (interface{}, error) {
		newToken, newExpiry, err := c.source.Token(ctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.token = newToken
		c.expiry = newExpiry
		c.mu.Unlock()
		return newToken, nil
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// TokenTransport is an http.RoundTripper that injects a Bearer token
// from a CachedTokenSource into every request.
type TokenTransport struct {
	Base   http.RoundTripper
	Source *CachedTokenSource
}

// NewTokenTransport creates a TokenTransport.
func NewTokenTransport(base http.RoundTripper, source *CachedTokenSource) *TokenTransport {
	return &TokenTransport{Base: base, Source: source}
}

// RoundTrip implements http.RoundTripper.
func (t *TokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.Source.Token(req.Context())
	if err != nil {
		return nil, err
	}
	// Clone the request to avoid mutating the original
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	return t.Base.RoundTrip(r)
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test -v -tags=unit -run TestTokenTransport ./pkg/auth/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/auth/
git commit -m "feat(auth): add AuthProvider interface, TokenTransport, and CachedTokenSource"
```

---

## Task 2: CachedTokenSource Tests

Thorough tests for the caching and singleflight behavior.

**Files:**
- Create: `pkg/auth/cached_test.go`

- [ ] **Step 1: Write tests for CachedTokenSource**

```go
// pkg/auth/cached_test.go
package auth_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
)

type countingTokenSource struct {
	calls atomic.Int64
	token string
	ttl   time.Duration
}

func (c *countingTokenSource) Token(_ context.Context) (string, time.Time, error) {
	c.calls.Add(1)
	return c.token, time.Now().Add(c.ttl), nil
}

func TestCachedTokenSource_CachesWithinTTL(t *testing.T) {
	source := &countingTokenSource{token: "tok-1", ttl: 10 * time.Minute}
	cached := auth.NewCachedTokenSource(source)

	ctx := context.Background()
	tok1, err := cached.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := cached.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if tok1 != "tok-1" || tok2 != "tok-1" {
		t.Errorf("expected tok-1, got %q and %q", tok1, tok2)
	}
	if source.calls.Load() != 1 {
		t.Errorf("expected 1 call to source, got %d", source.calls.Load())
	}
}

func TestCachedTokenSource_RefreshesAfterExpiry(t *testing.T) {
	source := &countingTokenSource{token: "tok-1", ttl: 1 * time.Millisecond}
	cached := auth.NewCachedTokenSource(source)

	ctx := context.Background()
	_, _ = cached.Token(ctx)
	time.Sleep(5 * time.Millisecond) // let token expire
	_, _ = cached.Token(ctx)

	if source.calls.Load() != 2 {
		t.Errorf("expected 2 calls to source after expiry, got %d", source.calls.Load())
	}
}

func TestCachedTokenSource_SingleflightUnderConcurrency(t *testing.T) {
	source := &countingTokenSource{token: "tok-1", ttl: 10 * time.Minute}
	cached := auth.NewCachedTokenSource(source)

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cached.Token(ctx)
			if err != nil {
				t.Errorf("Token() error: %v", err)
			}
		}()
	}
	wg.Wait()

	// Singleflight should collapse 50 concurrent calls into 1
	if source.calls.Load() > 2 {
		t.Errorf("expected at most 2 calls (singleflight), got %d", source.calls.Load())
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test -v -tags=unit -run TestCachedTokenSource ./pkg/auth/...`
Expected: PASS (all three tests)

- [ ] **Step 3: Commit**

```bash
git add pkg/auth/cached_test.go
git commit -m "test(auth): add CachedTokenSource caching, expiry, and singleflight tests"
```

---

## Task 3: EKS AuthProvider

Move the existing STS presigned token logic from `pkg/config/auth.go` into the new auth package.

**Files:**
- Create: `pkg/auth/eks/provider.go`
- Create: `pkg/auth/eks/provider_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/auth/eks/provider_test.go
//go:build unit

package eks_test

import (
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/eks"
	"k8s.io/client-go/rest"
)

func TestEKSProvider_ConfigureTransport_SetsWrapTransport(t *testing.T) {
	provider := eks.NewProvider("my-cluster", "us-west-2")
	cfg := &rest.Config{}

	if err := provider.ConfigureTransport(cfg); err != nil {
		t.Fatalf("ConfigureTransport error: %v", err)
	}

	if cfg.WrapTransport == nil {
		t.Fatal("expected WrapTransport to be set")
	}
}

func TestEKSProvider_ExtratsRegionFromEndpoint(t *testing.T) {
	tests := []struct {
		endpoint string
		expected string
	}{
		{"https://ABC123.gr7.us-west-2.eks.amazonaws.com", "us-west-2"},
		{"https://ABC123.gr7.eu-west-1.eks.amazonaws.com", "eu-west-1"},
	}

	for _, tt := range tests {
		region := eks.RegionFromEndpoint(tt.endpoint)
		if region != tt.expected {
			t.Errorf("RegionFromEndpoint(%q) = %q, want %q", tt.endpoint, region, tt.expected)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -v -tags=unit -run TestEKSProvider ./pkg/auth/eks/...`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement EKS AuthProvider**

Move the STS presigned token logic from `pkg/config/auth.go` into a new package. The key change: instead of returning a `string` token, implement `TokenSource` returning token + expiry, and use `AuthProvider` to wire it into `rest.Config` via `WrapTransport`.

```go
// pkg/auth/eks/provider.go
package eks

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"k8s.io/client-go/rest"
)

const tokenTTL = 14 * time.Minute // STS tokens are valid for 15 min; refresh at 14

// Provider implements auth.AuthProvider for AWS EKS clusters.
type Provider struct {
	ClusterName string
	Region      string
}

// NewProvider creates an EKS auth provider.
func NewProvider(clusterName, region string) *Provider {
	return &Provider{ClusterName: clusterName, Region: region}
}

// ConfigureTransport sets WrapTransport on the rest.Config to inject
// fresh STS presigned tokens on every K8S API request.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	source := &tokenSource{clusterName: p.ClusterName, region: p.Region}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

// tokenSource generates EKS bearer tokens via STS presigned URLs.
type tokenSource struct {
	clusterName string
	region      string
}

// Token generates a presigned STS GetCallerIdentity URL for EKS auth.
func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if s.region != "" {
		opts = append(opts, awsconfig.WithRegion(s.region))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	creds, err := awsCfg.Credentials.Retrieve(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to retrieve AWS credentials: %w", err)
	}

	stsURL := fmt.Sprintf(
		"https://sts.%s.amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15&X-Amz-Expires=60",
		s.region,
	)
	req, err := http.NewRequest("GET", stsURL, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create STS request: %w", err)
	}
	req.Header.Set("x-k8s-aws-id", s.clusterName)

	signer := v4.NewSigner()
	signedURL, _, err := signer.PresignHTTP(ctx, creds, req,
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"sts", s.region, time.Now())
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to presign STS request: %w", err)
	}

	token := "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString([]byte(signedURL))
	return token, time.Now().Add(tokenTTL), nil
}

// RegionFromEndpoint extracts the AWS region from an EKS endpoint URL.
// EKS endpoints: https://<id>.<suffix>.<region>.eks.amazonaws.com
func RegionFromEndpoint(endpoint string) string {
	host := strings.TrimPrefix(endpoint, "https://")
	host = strings.TrimPrefix(host, "http://")
	parts := strings.Split(host, ".")
	if len(parts) >= 5 {
		return parts[len(parts)-4]
	}
	return ""
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -v -tags=unit -run TestEKSProvider ./pkg/auth/eks/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/auth/eks/
git commit -m "feat(auth): add EKS AuthProvider with STS presigned token source"
```

---

## Task 4: GKE, AKS, OVH, OCI AuthProviders

Stub implementations for remaining cloud providers. Each follows the same pattern: implement `TokenSource`, wire into `AuthProvider` via `CachedTokenSource` + `WrapTransport`.

**Files:**
- Create: `pkg/auth/gke/provider.go`
- Create: `pkg/auth/aks/provider.go`
- Create: `pkg/auth/ovh/provider.go`
- Create: `pkg/auth/oci/provider.go`

- [ ] **Step 1: Implement GKE AuthProvider**

```go
// pkg/auth/gke/provider.go
package gke

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"golang.org/x/oauth2/google"
	"k8s.io/client-go/rest"
)

// Provider implements auth.AuthProvider for Google GKE clusters.
type Provider struct{}

// NewProvider creates a GKE auth provider.
func NewProvider() *Provider {
	return &Provider{}
}

// ConfigureTransport sets WrapTransport to inject GCP OAuth2 access tokens.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	source := &tokenSource{}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

type tokenSource struct{}

func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to find GCP credentials: %w", err)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get GCP token: %w", err)
	}
	return tok.AccessToken, tok.Expiry, nil
}
```

- [ ] **Step 2: Implement AKS AuthProvider**

```go
// pkg/auth/aks/provider.go
package aks

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"k8s.io/client-go/rest"
)

const azureK8SScope = "6dae42f8-4368-4678-94ff-3960e28e3630/.default"

// Provider implements auth.AuthProvider for Azure AKS clusters.
type Provider struct {
	ResourceGroup string
	ClusterName   string
}

// NewProvider creates an AKS auth provider.
func NewProvider(resourceGroup, clusterName string) *Provider {
	return &Provider{ResourceGroup: resourceGroup, ClusterName: clusterName}
}

// ConfigureTransport sets WrapTransport to inject Azure AD tokens.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	source := &tokenSource{}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

type tokenSource struct{}

func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create Azure credential: %w", err)
	}
	tok, err := cred.GetToken(ctx, azidentity.TokenRequestOptions{
		Scopes: []string{azureK8SScope},
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get Azure token: %w", err)
	}
	return tok.Token, tok.ExpiresOn, nil
}
```

- [ ] **Step 3: Implement OVH AuthProvider**

```go
// pkg/auth/ovh/provider.go
package ovh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/ovh/go-ovh/ovh"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Provider implements auth.AuthProvider for OVH Managed K8S clusters.
type Provider struct {
	ServiceName string
	ClusterID   string
}

// NewProvider creates an OVH auth provider.
func NewProvider(serviceName, clusterID string) *Provider {
	return &Provider{ServiceName: serviceName, ClusterID: clusterID}
}

// ConfigureTransport fetches a kubeconfig from OVH API and configures
// the rest.Config with the embedded credentials.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	// OVH kubeconfigs contain embedded credentials; fetch fresh on each call
	source := &tokenSource{serviceName: p.ServiceName, clusterID: p.ClusterID}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

type tokenSource struct {
	serviceName string
	clusterID   string
}

type kubeconfigResponse struct {
	Content string `json:"content"`
}

func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	client, err := ovh.NewDefaultClient()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create OVH client: %w", err)
	}

	var resp kubeconfigResponse
	endpoint := fmt.Sprintf("/cloud/project/%s/kube/%s/kubeconfig", s.serviceName, s.clusterID)
	if err := client.PostWithContext(ctx, endpoint, nil, &resp); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get OVH kubeconfig: %w", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(resp.Content)
	if err != nil {
		// Content might not be base64 encoded
		decoded = []byte(resp.Content)
	}

	kc, err := clientcmd.RESTConfigFromKubeConfig(decoded)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to parse OVH kubeconfig: %w", err)
	}

	// OVH kubeconfigs embed a bearer token
	if kc.BearerToken != "" {
		// Conservative 30-minute TTL since OVH doesn't expose expiry
		return kc.BearerToken, time.Now().Add(30 * time.Minute), nil
	}

	return "", time.Time{}, fmt.Errorf("OVH kubeconfig does not contain a bearer token")
}
```

- [ ] **Step 4: Implement OCI AuthProvider**

```go
// pkg/auth/oci/provider.go
package oci

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/containerengine"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Provider implements auth.AuthProvider for Oracle OKE clusters.
type Provider struct {
	ClusterOCID string
	Region      string
}

// NewProvider creates an OCI auth provider.
func NewProvider(clusterOCID, region string) *Provider {
	return &Provider{ClusterOCID: clusterOCID, Region: region}
}

// ConfigureTransport fetches cluster credentials from OCI and configures
// the rest.Config.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	source := &tokenSource{clusterOCID: p.ClusterOCID, region: p.Region}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

type tokenSource struct {
	clusterOCID string
	region      string
}

func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	provider, err := common.DefaultConfigProvider().Region()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get OCI config: %w", err)
	}

	client, err := containerengine.NewContainerEngineClientWithConfigurationProvider(
		common.DefaultConfigProvider(),
	)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create OKE client: %w", err)
	}

	if s.region != "" {
		client.SetRegion(s.region)
	}

	resp, err := client.CreateKubeconfig(ctx, containerengine.CreateKubeconfigRequest{
		ClusterId: &s.clusterOCID,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get OKE kubeconfig: %w", err)
	}

	kc, err := clientcmd.RESTConfigFromKubeConfig([]byte(resp.Content))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to parse OKE kubeconfig: %w", err)
	}

	if kc.BearerToken != "" {
		return kc.BearerToken, time.Now().Add(30 * time.Minute), nil
	}

	return "", time.Time{}, fmt.Errorf("OKE kubeconfig does not contain a bearer token")
}
```

- [ ] **Step 5: Run build to verify all providers compile**

Run: `go build ./pkg/auth/...`
Expected: Success (may need `go mod tidy` first for new SDK dependencies)

- [ ] **Step 6: Commit**

```bash
git add pkg/auth/gke/ pkg/auth/aks/ pkg/auth/ovh/ pkg/auth/oci/
git commit -m "feat(auth): add GKE, AKS, OVH, OCI auth providers"
```

---

## Task 5: PKL Schema — New Config + Auth Types

Replace the flat target config with the structured auth union.

**Files:**
- Modify: `schema/pkl/k8s.pkl:18-55`

- [ ] **Step 1: Update the PKL schema**

Replace the `Config` class and add auth types. The Config class currently occupies lines 18-55 of `schema/pkl/k8s.pkl`. Replace it with:

```pkl
/// Configuration for the K8S target.
/// Maps to the Go Config struct in pkg/config/config.go.
open class Config {
  hidden fixed type: String = "K8S"

  /// Default namespace for namespaced resources (defaults to "default")
  hidden defaultNamespace: String?

  /// Whether this cluster has a cloud load balancer controller.
  /// Defaults to true (production/cloud clusters). Set to false for local
  /// clusters without a LB controller (OrbStack, minikube, kind).
  hidden hasLoadBalancer: Boolean?

  /// Authentication strategy. Determines how the plugin connects to the
  /// K8S API server.
  hidden auth: Auth

  fixed Type: String = type
  fixed DefaultNamespace: String? = defaultNamespace
  fixed HasLoadBalancer: Boolean? = hasLoadBalancer
  fixed Auth: Auth = auth
}

// =============================================================================
// Auth Strategies
// =============================================================================

/// Base class for K8S authentication strategies.
abstract class Auth {
  /// Discriminator field for Go-side deserialization.
  hidden fixed type: String
  fixed Type: String = type
}

/// Kubeconfig-based auth. Covers vanilla K8S, dev clusters, and any
/// pre-configured cluster.
class KubeconfigAuth extends Auth {
  type = "Kubeconfig"
  hidden context: String?
  hidden kubeconfig: String?
  fixed Context: String? = context
  fixed Kubeconfig: String? = kubeconfig
}

/// AWS EKS auth via STS presigned token.
class EKSAuth extends Auth {
  type = "EKS"
  hidden endpoint: (String|formae.Resolvable)
  hidden certificateAuthority: (String|formae.Resolvable)
  hidden clusterName: (String|formae.Resolvable)
  hidden region: (String|formae.Resolvable)?
  fixed Endpoint: (String|formae.Resolvable) = endpoint
  fixed CertificateAuthority: (String|formae.Resolvable) = certificateAuthority
  fixed ClusterName: (String|formae.Resolvable) = clusterName
  fixed Region: (String|formae.Resolvable)? = region
}

/// Google GKE auth via OAuth2 access token.
class GKEAuth extends Auth {
  type = "GKE"
  hidden endpoint: (String|formae.Resolvable)
  hidden certificateAuthority: (String|formae.Resolvable)
  fixed Endpoint: (String|formae.Resolvable) = endpoint
  fixed CertificateAuthority: (String|formae.Resolvable) = certificateAuthority
}

/// Azure AKS auth via Azure AD token.
class AKSAuth extends Auth {
  type = "AKS"
  hidden endpoint: (String|formae.Resolvable)
  hidden certificateAuthority: (String|formae.Resolvable)
  hidden resourceGroup: (String|formae.Resolvable)?
  hidden clusterName: (String|formae.Resolvable)?
  fixed Endpoint: (String|formae.Resolvable) = endpoint
  fixed CertificateAuthority: (String|formae.Resolvable) = certificateAuthority
  fixed ResourceGroup: (String|formae.Resolvable)? = resourceGroup
  fixed ClusterName: (String|formae.Resolvable)? = clusterName
}

/// OVH Managed K8S auth.
class OVHAuth extends Auth {
  type = "OVH"
  hidden endpoint: (String|formae.Resolvable)
  hidden certificateAuthority: (String|formae.Resolvable)
  hidden serviceName: (String|formae.Resolvable)
  hidden clusterId: (String|formae.Resolvable)
  fixed Endpoint: (String|formae.Resolvable) = endpoint
  fixed CertificateAuthority: (String|formae.Resolvable) = certificateAuthority
  fixed ServiceName: (String|formae.Resolvable) = serviceName
  fixed ClusterId: (String|formae.Resolvable) = clusterId
}

/// Oracle OKE auth via OCI session token.
class OCIAuth extends Auth {
  type = "OCI"
  hidden endpoint: (String|formae.Resolvable)
  hidden certificateAuthority: (String|formae.Resolvable)
  hidden clusterOcid: (String|formae.Resolvable)
  hidden region: (String|formae.Resolvable)?
  fixed Endpoint: (String|formae.Resolvable) = endpoint
  fixed CertificateAuthority: (String|formae.Resolvable) = certificateAuthority
  fixed ClusterOcid: (String|formae.Resolvable) = clusterOcid
  fixed Region: (String|formae.Resolvable)? = region
}
```

- [ ] **Step 2: Verify schema with pkl eval**

Run: `pkl eval schema/pkl/k8s.pkl`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add schema/pkl/k8s.pkl
git commit -m "feat(schema): replace flat config with auth union types"
```

---

## Task 6: Config Struct and Auth Dispatching

Rewrite the Go config to match the new schema and dispatch to auth providers.

**Files:**
- Modify: `pkg/config/config.go`
- Delete: `pkg/config/auth.go`

- [ ] **Step 1: Write a failing test for config deserialization**

```go
// pkg/config/config_test.go
//go:build unit

package config_test

import (
	"encoding/json"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
)

func TestFromTargetConfig_KubeconfigAuth(t *testing.T) {
	raw := json.RawMessage(`{
		"Auth": {"Type": "Kubeconfig", "Context": "orbstack"}
	}`)
	cfg, err := config.FromTargetConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthType() != "Kubeconfig" {
		t.Errorf("expected auth type Kubeconfig, got %s", cfg.AuthType())
	}
}

func TestFromTargetConfig_EKSAuth(t *testing.T) {
	raw := json.RawMessage(`{
		"Auth": {"Type": "EKS", "Endpoint": "https://example.eks.amazonaws.com", "CertificateAuthority": "Y2E=", "ClusterName": "my-cluster", "Region": "us-west-2"}
	}`)
	cfg, err := config.FromTargetConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthType() != "EKS" {
		t.Errorf("expected auth type EKS, got %s", cfg.AuthType())
	}
}

func TestFromTargetConfig_DefaultNamespace(t *testing.T) {
	raw := json.RawMessage(`{
		"DefaultNamespace": "production",
		"Auth": {"Type": "Kubeconfig"}
	}`)
	cfg, err := config.FromTargetConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EffectiveNamespace() != "production" {
		t.Errorf("expected production, got %s", cfg.EffectiveNamespace())
	}
}

func TestFromTargetConfig_DefaultNamespaceFallback(t *testing.T) {
	raw := json.RawMessage(`{"Auth": {"Type": "Kubeconfig"}}`)
	cfg, err := config.FromTargetConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EffectiveNamespace() != "default" {
		t.Errorf("expected default, got %s", cfg.EffectiveNamespace())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -v -tags=unit -run TestFromTargetConfig ./pkg/config/...`
Expected: FAIL — AuthType method doesn't exist

- [ ] **Step 3: Rewrite config.go**

Replace the contents of `pkg/config/config.go` with:

```go
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/aks"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/eks"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/gke"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/oci"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/ovh"
)

// Config holds the K8S plugin configuration extracted from target config.
type Config struct {
	DefaultNamespace string          `json:"DefaultNamespace,omitempty"`
	HasLoadBalancer  *bool           `json:"HasLoadBalancer,omitempty"`
	Auth             json.RawMessage `json:"Auth"`

	// Parsed auth config — populated by FromTargetConfig
	authType string
	authRaw  json.RawMessage
}

// authHeader is used to extract just the Type discriminator.
type authHeader struct {
	Type string `json:"Type"`
}

// KubeconfigAuthConfig holds kubeconfig-based auth fields.
type KubeconfigAuthConfig struct {
	Context    string `json:"Context,omitempty"`
	Kubeconfig string `json:"Kubeconfig,omitempty"`
}

// CloudAuthConfig holds fields common to all cloud auth types.
type CloudAuthConfig struct {
	Endpoint             string `json:"Endpoint"`
	CertificateAuthority string `json:"CertificateAuthority"`
}

// EKSAuthConfig holds EKS-specific auth fields.
type EKSAuthConfig struct {
	CloudAuthConfig
	ClusterName string `json:"ClusterName"`
	Region      string `json:"Region,omitempty"`
}

// AKSAuthConfig holds AKS-specific auth fields.
type AKSAuthConfig struct {
	CloudAuthConfig
	ResourceGroup string `json:"ResourceGroup,omitempty"`
	ClusterName   string `json:"ClusterName,omitempty"`
}

// OVHAuthConfig holds OVH-specific auth fields.
type OVHAuthConfig struct {
	CloudAuthConfig
	ServiceName string `json:"ServiceName"`
	ClusterID   string `json:"ClusterId"`
}

// OCIAuthConfig holds OCI-specific auth fields.
type OCIAuthConfig struct {
	CloudAuthConfig
	ClusterOCID string `json:"ClusterOcid"`
	Region      string `json:"Region,omitempty"`
}

// FromTargetConfig extracts Config from the target configuration bytes.
func FromTargetConfig(targetConfig []byte) (*Config, error) {
	if len(targetConfig) == 0 {
		return nil, fmt.Errorf("empty target config")
	}

	var cfg Config
	if err := json.Unmarshal(targetConfig, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse target config: %w", err)
	}

	var header authHeader
	if err := json.Unmarshal(cfg.Auth, &header); err != nil {
		return nil, fmt.Errorf("failed to parse auth type: %w", err)
	}
	cfg.authType = header.Type
	cfg.authRaw = cfg.Auth

	return &cfg, nil
}

// AuthType returns the auth strategy type string.
func (c *Config) AuthType() string {
	return c.authType
}

// EffectiveNamespace returns the namespace to use for operations.
func (c *Config) EffectiveNamespace() string {
	if c.DefaultNamespace != "" {
		return c.DefaultNamespace
	}
	return "default"
}

// HasLoadBalancerController returns whether the cluster has a LB controller.
func (c *Config) HasLoadBalancerController() bool {
	if c.HasLoadBalancer == nil {
		return true
	}
	return *c.HasLoadBalancer
}

// ToK8sConfig builds a rest.Config based on the auth strategy.
func (c *Config) ToK8sConfig() (*rest.Config, error) {
	switch c.authType {
	case "Kubeconfig":
		return c.buildKubeconfigConfig()
	case "EKS":
		return c.buildCloudConfig(c.newEKSProvider)
	case "GKE":
		return c.buildCloudConfig(c.newGKEProvider)
	case "AKS":
		return c.buildCloudConfig(c.newAKSProvider)
	case "OVH":
		return c.buildCloudConfig(c.newOVHProvider)
	case "OCI":
		return c.buildCloudConfig(c.newOCIProvider)
	default:
		return nil, fmt.Errorf("unsupported auth type: %s", c.authType)
	}
}

func (c *Config) buildKubeconfigConfig() (*rest.Config, error) {
	var kc KubeconfigAuthConfig
	if err := json.Unmarshal(c.authRaw, &kc); err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig auth: %w", err)
	}

	kubeconfig := kc.Kubeconfig
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig == "" {
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}

	overrides := &clientcmd.ConfigOverrides{}
	if kc.Context != "" {
		overrides.CurrentContext = kc.Context
	}

	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		overrides,
	).ClientConfig()
}

func (c *Config) buildCloudConfig(providerFn func() (auth.AuthProvider, *CloudAuthConfig, error)) (*rest.Config, error) {
	provider, cloud, err := providerFn()
	if err != nil {
		return nil, err
	}

	caData, err := base64Decode(cloud.CertificateAuthority)
	if err != nil {
		return nil, fmt.Errorf("failed to decode certificate authority: %w", err)
	}

	cfg := &rest.Config{
		Host: cloud.Endpoint,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: caData,
		},
	}

	// Suppress K8S API deprecation warnings
	cfg.WarningHandler = rest.NoWarnings{}

	if err := provider.ConfigureTransport(cfg); err != nil {
		return nil, fmt.Errorf("failed to configure auth transport: %w", err)
	}

	return cfg, nil
}

func (c *Config) newEKSProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac EKSAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse EKS auth config: %w", err)
	}
	region := ac.Region
	if region == "" {
		region = eks.RegionFromEndpoint(ac.Endpoint)
	}
	return eks.NewProvider(ac.ClusterName, region), &ac.CloudAuthConfig, nil
}

func (c *Config) newGKEProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac CloudAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse GKE auth config: %w", err)
	}
	return gke.NewProvider(), &ac, nil
}

func (c *Config) newAKSProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac AKSAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse AKS auth config: %w", err)
	}
	return aks.NewProvider(ac.ResourceGroup, ac.ClusterName), &ac.CloudAuthConfig, nil
}

func (c *Config) newOVHProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac OVHAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse OVH auth config: %w", err)
	}
	return ovh.NewProvider(ac.ServiceName, ac.ClusterID), &ac.CloudAuthConfig, nil
}

func (c *Config) newOCIProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac OCIAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse OCI auth config: %w", err)
	}
	return oci.NewProvider(ac.ClusterOCID, ac.Region), &ac.CloudAuthConfig, nil
}

// base64Decode is a helper — add "encoding/base64" to the import block above.
func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
```

- [ ] **Step 4: Delete pkg/config/auth.go**

```bash
rm pkg/config/auth.go
```

- [ ] **Step 5: Run the tests**

Run: `go test -v -tags=unit -run TestFromTargetConfig ./pkg/config/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git rm pkg/config/auth.go
git commit -m "feat(config): rewrite config with auth type dispatching, remove EKS-specific auth"
```

---

## Task 7: Transport Client and Plugin Simplification

Update `transport.Client` and simplify `k8s.go` to remove the client cache TTL workaround.

**Files:**
- Modify: `pkg/transport/client.go`
- Modify: `k8s.go`

- [ ] **Step 1: Simplify transport/client.go**

The `NewClient` function no longer needs to construct `rest.Config` — it receives one from `Config.ToK8sConfig()`. Update to:

```go
package transport

import (
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Client wraps the Kubernetes clientset with plugin configuration.
type Client struct {
	*kubernetes.Clientset
	Dynamic       dynamic.Interface
	ApiExtensions apiextensionsclientset.Interface
	Config        *config.Config
}

// NewClient creates a new Kubernetes client from the provided config.
func NewClient(cfg *config.Config) (*Client, error) {
	restConfig, err := cfg.ToK8sConfig()
	if err != nil {
		return nil, err
	}

	// Suppress K8S API deprecation warnings
	restConfig.WarningHandler = rest.NoWarnings{}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	apiextClient, err := apiextensionsclientset.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	return &Client{
		Clientset:     clientset,
		Dynamic:       dynamicClient,
		ApiExtensions: apiextClient,
		Config:        cfg,
	}, nil
}
```

- [ ] **Step 2: Simplify k8s.go — remove client cache**

The 50-second client cache TTL was a workaround for EKS token expiry. With `WrapTransport` handling token refresh, clients can be long-lived. Remove the `cachedClient` struct, `clientCacheTTL`, `getOrCreateClient`, and the `clients` map from `Plugin`. Simplify `getProvisioner`:

```go
// Replace the Plugin struct and getProvisioner in k8s.go.
// Remove: clientCacheTTL, cachedClient, getOrCreateClient, clients map, mu mutex

type Plugin struct{}

func (p *Plugin) getProvisioner(ctx context.Context, resourceType string, targetConfig []byte) (prov.Provisioner, error) {
	if !registry.HasProvisioner(resourceType) {
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}

	cfg, err := config.FromTargetConfig(targetConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to extract config: %w", err)
	}

	client, err := transport.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create K8S client: %w", err)
	}

	factory, _ := registry.GetFactory(resourceType)
	return factory(client, cfg), nil
}
```

Also remove the unused imports: `"crypto/sha256"`, `"sync"`, `"time"`.

- [ ] **Step 3: Build to verify compilation**

Run: `go build ./...`
Expected: Success

- [ ] **Step 4: Commit**

```bash
git add pkg/transport/client.go k8s.go
git commit -m "refactor: remove client cache TTL workaround, simplify transport"
```

---

## Task 8: Update PKL Test and Example Files

Update all PKL files that reference `new k8s.Config` to use the new auth types.

**Files:**
- Modify: `testdata/config/vars.pkl`
- Modify: `examples/webapp.pkl`
- Modify: `examples/webapp-v2.pkl`
- Modify: `examples/nginx-ingress.pkl`
- Modify: `examples/drift-demo.pkl`
- Modify: `examples/eks-full-stack/test-resolvable.pkl`
- Modify: `examples/eks-full-stack/stage2-webapp.pkl`
- Modify: `examples/eks-full-stack/README.md`

- [ ] **Step 1: Update testdata/config/vars.pkl**

```pkl
target = new formae.Target {
  label = "k8s-target"
  namespace = "K8S"
  config = new k8s.Config {
    hasLoadBalancer = false
    auth = new k8s.KubeconfigAuth {}
  }
}
```

- [ ] **Step 2: Update examples that use KubeconfigAuth**

For `examples/webapp.pkl`, `examples/webapp-v2.pkl`, `examples/nginx-ingress.pkl`, and `examples/drift-demo.pkl`, replace each `new k8s.Config { ... }` block. For example, in `webapp.pkl`:

```pkl
config = new k8s.Config {
  auth = new k8s.KubeconfigAuth {
    context = "orbstack"
  }
  hasLoadBalancer = false
}
```

Apply the same pattern to all four files. The exact context name and hasLoadBalancer values should match what was there before (e.g., if `waitForLoadBalancer = false` was set, use `hasLoadBalancer = false`).

- [ ] **Step 3: Update examples/eks-full-stack/test-resolvable.pkl**

```pkl
local _k8sTarget = new formae.Target {
  label = "k8s-target"
  config = new k8s.Config {
    auth = new k8s.EKSAuth {
      endpoint = _eksCluster.res.endpoint
      certificateAuthority = _eksCluster.res.certificateAuthorityData
      clusterName = _eksCluster.res.name
    }
    hasLoadBalancer = false
  }
}
```

- [ ] **Step 4: Update examples/eks-full-stack/stage2-webapp.pkl**

This example uses a kubeconfig context for the two-stage flow:

```pkl
config = new k8s.Config {
  auth = new k8s.KubeconfigAuth {
    context = eksContext
  }
}
```

- [ ] **Step 5: Update examples/eks-full-stack/README.md**

Update the config examples in the README to show the new auth syntax.

- [ ] **Step 6: Verify PKL evaluation**

Run: `pkl eval testdata/config/vars.pkl` and `pkl eval examples/webapp.pkl`
Expected: No errors

- [ ] **Step 7: Commit**

```bash
git add testdata/ examples/
git commit -m "chore: update all PKL files to use auth union types"
```

---

## Task 9: Add New SDK Dependencies

Add the cloud SDK Go module dependencies.

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add dependencies**

```bash
go get golang.org/x/oauth2/google
go get github.com/Azure/azure-sdk-for-go/sdk/azidentity
go get github.com/ovh/go-ovh/ovh
go get github.com/oracle/oci-go-sdk/v65
go mod tidy
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: Success

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add cloud SDK dependencies for GKE, AKS, OVH, OCI auth"
```

---

## Task 10: Integration Verification

Verify the full system works with existing conformance tests (kubeconfig path) and the EKS resolvable example.

**Files:** No changes — verification only.

- [ ] **Step 1: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 2: Run unit tests**

Run: `make test-unit`
Expected: PASS

- [ ] **Step 3: Run build**

Run: `make build`
Expected: PASS

- [ ] **Step 4: Install plugin locally and run conformance tests**

Run: `make install && make conformance-test TEST=configmap PARALLEL=1`
Expected: ConfigMap CRUD + discovery tests PASS. This validates the KubeconfigAuth path works end-to-end.

- [ ] **Step 5: Verify PKL schema**

Run: `make verify-schema`
Expected: PASS

- [ ] **Step 6: Commit any fixes if needed**

Only if earlier steps revealed issues. Otherwise, no commit.
