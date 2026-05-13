// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"fmt"
	"sync"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
)

// cacheTTL is the maximum lifetime of a cached *Client before it is forcibly
// rebuilt. It is intentionally shorter than the shortest auth-token TTL we
// support (EKS at 14 min, OCI at 5 min) so that a misbehaving CachedTokenSource
// — or a Provider whose underlying SDK credential rotated outside our view —
// cannot serve a stale token for longer than this window.
//
// Token-level refresh is still handled inside CachedTokenSource on every call;
// this TTL is the outer safety net.
const cacheTTL = 10 * time.Minute

// entry is a cached client paired with its insertion time. We keep the time
// separately from any field on *Client to avoid coupling the cache to the
// client's internals.
type entry struct {
	client    *Client
	createdAt time.Time
}

// clientCache is a process-wide cache of *Client instances keyed on
// config.CacheKey(). It deduplicates expensive per-CRUD work:
//
//   - EKS / OCI: STS / OCI signature presigning
//   - GKE: ADC discovery + OAuth2 token mint
//   - AKS: full Azure credential chain (env, IMDS, CLI fallback)
//   - OVH: synchronous POST that re-issues a kubeconfig
//
// Without this cache, every Create/Read/Update/Delete/Status/List call
// re-walks the entire credential chain. Some of those paths (notably OVH's
// kubeconfig POST) can rate-limit or revoke prior credentials, which makes
// the lack of caching a correctness problem and not just a performance one.
//
// Keying: config.CacheKey() collapses to (authType + endpoint + cluster
// identity). Two targets with identical keys share a *Client, including its
// in-memory CachedTokenSource. Two targets with different keys never alias.
//
// Eviction: an entry is rebuilt when it exceeds cacheTTL. We do not
// proactively close evicted clients — *kubernetes.Clientset has no Close()
// and its underlying http.RoundTripper relies on idle-connection eviction in
// http.DefaultTransport. A 10-minute TTL is short enough that long-lived
// goroutines from evicted clients are not a leak.
type clientCache struct {
	mu      sync.RWMutex
	entries map[string]entry
	build   clientBuilder
	ttl     time.Duration
}

// defaultCache is the process-wide cache instance the package-level
// CachedNewClient uses. It is intentionally unexported; tests construct
// their own via newClientCache.
var defaultCache = newClientCache(NewClient)

// clientBuilder builds a *Client from a *config.Config. Production uses
// NewClient; tests substitute a fake.
type clientBuilder func(*config.Config) (*Client, error)

func newClientCache(build clientBuilder) *clientCache {
	return &clientCache{
		entries: make(map[string]entry),
		build:   build,
		ttl:     cacheTTL,
	}
}

// get returns a cached client if one exists and has not expired.
// The second return value is true on hit, false on miss-or-expired.
func (c *clientCache) get(key string, now time.Time) (*Client, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if now.Sub(e.createdAt) >= c.ttl {
		return nil, false
	}
	return e.client, true
}

// put inserts client under key, overwriting any expired entry. Callers must
// not hold cache.mu when calling put.
func (c *clientCache) put(key string, client *Client, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry{client: client, createdAt: now}
}


// CachedNewClient returns a process-wide cached *Client for cfg, or builds
// and caches a new one if no live entry exists for cfg.CacheKey().
//
// Callers MUST use this entry point rather than NewClient when a Config is
// available — NewClient is retained for tests and for the rare case where
// caching is undesirable (e.g. a one-shot integration test).
func CachedNewClient(cfg *config.Config) (*Client, error) {
	return defaultCache.fetch(cfg)
}

func (c *clientCache) fetch(cfg *config.Config) (*Client, error) {
	key, err := cfg.CacheKey()
	if err != nil {
		return nil, fmt.Errorf("client cache: %w", err)
	}

	now := time.Now()
	if client, ok := c.get(key, now); ok {
		return client, nil
	}

	// Build outside the lock. Two goroutines may both miss the cache and
	// each build a client; the loser's *Client is GC'd. That's fine: the
	// cost of double-building is bounded by the slowest token mint (<1s
	// typically, capped at 10s by the per-provider context timeout), and
	// using a singleflight here would couple this cache to a global
	// goroutine pool we don't otherwise need.
	client, err := c.build(cfg)
	if err != nil {
		return nil, err
	}
	c.put(key, client, time.Now())
	return client, nil
}
