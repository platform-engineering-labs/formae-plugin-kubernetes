// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package aks_test

import (
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/aks"
	"k8s.io/client-go/rest"
)

func TestAKSProvider_CarriesClusterIdentity(t *testing.T) {
	p := aks.NewProvider("rg-prod", "aks-prod", "")
	if p.ResourceGroup != "rg-prod" {
		t.Errorf("ResourceGroup = %q, want rg-prod", p.ResourceGroup)
	}
	if p.ClusterName != "aks-prod" {
		t.Errorf("ClusterName = %q, want aks-prod", p.ClusterName)
	}
}

func TestAKSProvider_DefaultScope(t *testing.T) {
	// Empty Scope is expected to fall through to DefaultAKSScope at
	// token-request time. We only assert the field is preserved as empty
	// on construction; the default is applied in ConfigureTransport.
	p := aks.NewProvider("rg", "c", "")
	if p.Scope != "" {
		t.Errorf("Scope should default to empty (resolved later), got %q", p.Scope)
	}
}

func TestAKSProvider_ScopeOverride(t *testing.T) {
	const custom = "api://custom-aad-app/.default"
	p := aks.NewProvider("rg", "c", custom)
	if p.Scope != custom {
		t.Errorf("Scope = %q, want %q", p.Scope, custom)
	}
}

func TestAKSProvider_ConfigureTransport_SetsWrapTransport(t *testing.T) {
	provider := aks.NewProvider("rg", "c", "")
	cfg := &rest.Config{}
	if err := provider.ConfigureTransport(cfg); err != nil {
		t.Fatalf("ConfigureTransport: %v", err)
	}
	if cfg.WrapTransport == nil {
		t.Fatal("expected WrapTransport to be set")
	}
}
