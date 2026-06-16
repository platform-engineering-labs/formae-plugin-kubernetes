// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package custom

import (
	"context"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestList_DiscoveryNone_ReturnsNothing(t *testing.T) {
	// Default ("none") and explicit "none" both short-circuit before touching
	// the client, so a nil Client is safe here.
	for _, cfg := range []*config.Config{
		{},
		{CustomResourceDiscovery: "none"},
	} {
		c := &CustomResource{Config: cfg}
		res, err := c.List(context.Background(), &resource.ListRequest{})
		if err != nil {
			t.Fatalf("List error: %v", err)
		}
		if len(res.NativeIDs) != 0 {
			t.Fatalf("expected no native IDs, got %d", len(res.NativeIDs))
		}
	}
}

func TestDiscoveryMode(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"nil config", nil, discoveryNone},
		{"empty", &config.Config{}, discoveryNone},
		{"explicit none", &config.Config{CustomResourceDiscovery: "none"}, discoveryNone},
		{"explicit all", &config.Config{CustomResourceDiscovery: "all"}, discoveryAll},
		{"explicit groups", &config.Config{CustomResourceDiscovery: "groups"}, discoveryGroups},
		{"legacy bare allowlist", &config.Config{CustomResourceGroups: []string{"example.com"}}, discoveryGroups},
		{"unknown falls back to none", &config.Config{CustomResourceDiscovery: "bogus"}, discoveryNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &CustomResource{Config: tc.cfg}
			if got := c.discoveryMode(); got != tc.want {
				t.Fatalf("discoveryMode() = %q, want %q", got, tc.want)
			}
		})
	}
}
