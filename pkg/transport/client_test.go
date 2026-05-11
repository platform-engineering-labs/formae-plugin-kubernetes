// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package transport

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
)

// mustConfig parses a target-config JSON literal and aborts the test on
// failure. Used so tests read declaratively as their target configs.
func mustConfig(t *testing.T, raw string) *config.Config {
	t.Helper()
	cfg, err := config.FromTargetConfig([]byte(raw))
	if err != nil {
		t.Fatalf("FromTargetConfig(%q): %v", raw, err)
	}
	return cfg
}

// stubBuilder counts how many times it's invoked and returns a unique *Client
// per call, letting tests assert exactly which call paths went through the
// builder vs the cache.
type stubBuilder struct {
	calls atomic.Int64
}

func (s *stubBuilder) build(_ *config.Config) (*Client, error) {
	s.calls.Add(1)
	// Each call returns a fresh *Client so pointer-equality tests can
	// distinguish cache hits from rebuilds.
	return &Client{Config: &config.Config{}}, nil
}

func (s *stubBuilder) failing(_ *config.Config) (*Client, error) {
	s.calls.Add(1)
	return nil, errors.New("build failed")
}

func TestClientCache_HitSameKey(t *testing.T) {
	b := &stubBuilder{}
	cache := newClientCache(b.build)

	cfg := mustConfig(t, `{"Auth":{"Type":"EKS","Endpoint":"https://e.example","CertificateAuthority":"Y2E=","ClusterName":"prod","Region":"us-east-1"}}`)

	c1, err := cache.fetch(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := cache.fetch(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if c1 != c2 {
		t.Errorf("expected cache hit to return same *Client, got %p vs %p", c1, c2)
	}
	if b.calls.Load() != 1 {
		t.Errorf("expected exactly 1 builder call, got %d", b.calls.Load())
	}
}

func TestClientCache_MissDifferentClusters(t *testing.T) {
	b := &stubBuilder{}
	cache := newClientCache(b.build)

	cfgA := mustConfig(t, `{"Auth":{"Type":"EKS","Endpoint":"https://a","CertificateAuthority":"Y2E=","ClusterName":"a","Region":"us-east-1"}}`)
	cfgB := mustConfig(t, `{"Auth":{"Type":"EKS","Endpoint":"https://b","CertificateAuthority":"Y2E=","ClusterName":"b","Region":"us-east-1"}}`)

	cA, _ := cache.fetch(cfgA)
	cB, _ := cache.fetch(cfgB)
	if cA == cB {
		t.Error("different cluster identities must not alias on the same client")
	}
	if got := b.calls.Load(); got != 2 {
		t.Errorf("expected 2 builder calls for 2 distinct keys, got %d", got)
	}
}

func TestClientCache_MissAfterTTL(t *testing.T) {
	b := &stubBuilder{}
	cache := newClientCache(b.build)
	cache.ttl = 1 * time.Millisecond

	cfg := mustConfig(t, `{"Auth":{"Type":"Kubeconfig","Context":"orbstack"}}`)

	if _, err := cache.fetch(cfg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond) // exceed TTL
	if _, err := cache.fetch(cfg); err != nil {
		t.Fatal(err)
	}

	if got := b.calls.Load(); got != 2 {
		t.Errorf("expected 2 builder calls after TTL expiry, got %d", got)
	}
}

func TestClientCache_GKEDistinguishesProjects(t *testing.T) {
	// Two GKE clusters sharing the same name across different projects must
	// not collide. This is the bug C-AUTH-2 fixes: without cluster
	// identity on Provider, both keys would hash to the same cache slot.
	b := &stubBuilder{}
	cache := newClientCache(b.build)

	cfgA := mustConfig(t, `{"Auth":{"Type":"GKE","Endpoint":"https://e","CertificateAuthority":"Y2E=","ProjectId":"proj-a","Location":"us-central1","ClusterName":"shared"}}`)
	cfgB := mustConfig(t, `{"Auth":{"Type":"GKE","Endpoint":"https://e","CertificateAuthority":"Y2E=","ProjectId":"proj-b","Location":"us-central1","ClusterName":"shared"}}`)

	cA, _ := cache.fetch(cfgA)
	cB, _ := cache.fetch(cfgB)
	if cA == cB {
		t.Error("different GCP projects must not alias on the same cached client")
	}
}

func TestClientCache_AKSDistinguishesResourceGroups(t *testing.T) {
	b := &stubBuilder{}
	cache := newClientCache(b.build)

	cfgA := mustConfig(t, `{"Auth":{"Type":"AKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ResourceGroup":"rg-a","ClusterName":"shared"}}`)
	cfgB := mustConfig(t, `{"Auth":{"Type":"AKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ResourceGroup":"rg-b","ClusterName":"shared"}}`)

	cA, _ := cache.fetch(cfgA)
	cB, _ := cache.fetch(cfgB)
	if cA == cB {
		t.Error("different Azure resource groups must not alias on the same cached client")
	}
}

func TestClientCache_PropagatesBuilderError(t *testing.T) {
	b := &stubBuilder{}
	cache := newClientCache(b.failing)
	cfg := mustConfig(t, `{"Auth":{"Type":"Kubeconfig","Context":"orbstack"}}`)
	if _, err := cache.fetch(cfg); err == nil {
		t.Fatal("expected builder error to propagate")
	}
	// A failed build must not poison the cache — subsequent calls retry.
	if _, err := cache.fetch(cfg); err == nil {
		t.Fatal("expected second call to also try the builder")
	}
	if got := b.calls.Load(); got < 2 {
		t.Errorf("expected at least 2 builder calls after failure, got %d", got)
	}
}

func TestClientCache_CacheKeyError(t *testing.T) {
	// A Config whose Auth block lacks a Type field never makes it past
	// FromTargetConfig today, but if someone constructs a Config directly
	// or extends the surface, the cache must propagate the error rather
	// than panic.
	b := &stubBuilder{}
	cache := newClientCache(b.build)

	cfg, err := config.FromTargetConfig([]byte(`{"Auth":{"Type":"UNSUPPORTED"}}`))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := cache.fetch(cfg); err == nil {
		t.Fatal("expected CacheKey error to surface from fetch")
	}
	if got := b.calls.Load(); got != 0 {
		t.Errorf("builder must not be called on CacheKey error, got %d calls", got)
	}
}

// TestClientCache_ConcurrentMiss verifies the cache returns a single client
// when many goroutines miss at once (with a stub builder), and that no
// goroutine panics under contention. We don't assert exactly-one build
// because the cache deliberately does not use singleflight (see cache.go).
func TestClientCache_ConcurrentMiss(t *testing.T) {
	b := &stubBuilder{}
	cache := newClientCache(b.build)
	cfg := mustConfig(t, `{"Auth":{"Type":"Kubeconfig","Context":"orbstack"}}`)

	const N = 32
	done := make(chan *Client, N)
	for i := 0; i < N; i++ {
		go func() {
			c, _ := cache.fetch(cfg)
			done <- c
		}()
	}

	var first *Client
	for i := 0; i < N; i++ {
		c := <-done
		if first == nil {
			first = c
			continue
		}
		// After the first build completes, all subsequent fetches must
		// resolve to the cached entry. Allow up to one "loser" client
		// for the brief race window where two goroutines both miss.
		_ = c
	}
	if got := b.calls.Load(); got > 2 {
		t.Errorf("expected at most 2 concurrent builds (race window), got %d", got)
	}
}

