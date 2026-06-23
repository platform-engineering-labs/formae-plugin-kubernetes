// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// With a target-config version override, ResolveK8sVersion returns before any
// live Discovery call, so a Client with a nil Clientset resolves + caches fine.
func TestResolveVersion_OverrideAndCache(t *testing.T) {
	c := &Client{Config: &config.Config{KubernetesVersion: "1.33"}}

	v, err := c.ResolveVersion(context.Background())
	if err != nil || v != "1.33" {
		t.Fatalf("ResolveVersion = (%q, %v), want (1.33, nil)", v, err)
	}
	if !c.versionSet || c.version != "1.33" {
		t.Fatalf("expected cached version 1.33, got set=%v val=%q", c.versionSet, c.version)
	}
	// Second call returns the memoized value.
	if v2, _ := c.ResolveVersion(context.Background()); v2 != "1.33" {
		t.Fatalf("cached ResolveVersion = %q, want 1.33", v2)
	}
}

// A failed resolution must NOT be cached, so a transient apiserver blip doesn't
// permanently disable version gating for the life of the process-cached Client.
func TestResolveVersion_DoesNotCacheError(t *testing.T) {
	t.Setenv(config.EnvK8sVersion, "") // no env override
	// No config override + a dead apiserver endpoint → ServerVersion() errors.
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	c := &Client{Clientset: cs, Config: &config.Config{}}

	if _, err := c.ResolveVersion(context.Background()); err == nil {
		t.Fatal("expected error resolving version against a dead endpoint")
	}
	if c.versionSet {
		t.Fatal("a failed resolution must not be cached (versionSet should stay false)")
	}
	// A subsequent call retries (errors again) rather than returning a stale cache.
	if _, err := c.ResolveVersion(context.Background()); err == nil {
		t.Fatal("expected retry to error again, not a cached success")
	}
}
