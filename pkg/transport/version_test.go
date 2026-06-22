// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
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
