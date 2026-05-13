// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

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

func TestEKSProvider_RegionFromEndpoint(t *testing.T) {
	tests := []struct {
		endpoint string
		expected string
	}{
		{"https://ABC123.gr7.us-west-2.eks.amazonaws.com", "us-west-2"},
		{"https://ABC123.gr7.eu-west-1.eks.amazonaws.com", "eu-west-1"},
		{"https://short.url", ""},
	}

	for _, tt := range tests {
		region := eks.RegionFromEndpoint(tt.endpoint)
		if region != tt.expected {
			t.Errorf("RegionFromEndpoint(%q) = %q, want %q", tt.endpoint, region, tt.expected)
		}
	}
}
