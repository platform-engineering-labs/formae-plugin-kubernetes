// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

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

func TestFromTargetConfig_HasLoadBalancerDefaults(t *testing.T) {
	raw := json.RawMessage(`{"Auth": {"Type": "Kubeconfig"}}`)
	cfg, err := config.FromTargetConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HasLoadBalancerController() {
		t.Error("expected HasLoadBalancerController to default to true")
	}
}

func TestFromTargetConfig_HasLoadBalancerFalse(t *testing.T) {
	raw := json.RawMessage(`{"HasLoadBalancer": false, "Auth": {"Type": "Kubeconfig"}}`)
	cfg, err := config.FromTargetConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HasLoadBalancerController() {
		t.Error("expected HasLoadBalancerController to be false")
	}
}
