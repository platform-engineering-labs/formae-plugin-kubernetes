// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package config_test

import (
	"encoding/json"
	"strings"
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

// TestFromTargetConfig_MissingAuthBlock covers H-CFG-1: a target config
// that forgets the Auth block (or sets it to null) must return a clear
// error, not a json.Unmarshal "unexpected end of JSON input".
func TestFromTargetConfig_MissingAuthBlock(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty object", `{}`},
		{"null auth", `{"Auth": null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.FromTargetConfig([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected error for missing Auth block")
			}
			if !strings.Contains(err.Error(), "missing required Auth") {
				t.Errorf("error should name the missing field, got %q", err.Error())
			}
		})
	}
}

// TestFromTargetConfig_MissingAuthType covers a related malformation: Auth
// present but no Type discriminator. The caller hits the default branch in
// ToK8sConfig with an opaque message; surface it early.
func TestFromTargetConfig_MissingAuthType(t *testing.T) {
	_, err := config.FromTargetConfig([]byte(`{"Auth": {"Context": "orbstack"}}`))
	if err == nil {
		t.Fatal("expected error for missing Type field")
	}
	if !strings.Contains(err.Error(), "Type") {
		t.Errorf("error should mention Type, got %q", err.Error())
	}
}

// TestToK8sConfig_KubeconfigEmptyHomeAndEnv covers H-CFG-2: when none of
// Auth.Kubeconfig, $KUBECONFIG, or $HOME are set, ToK8sConfig must return
// a clear actionable error rather than silently falling through to
// in-cluster auth (which on dev machines produces a cryptic clientcmd
// error).
func TestToK8sConfig_KubeconfigEmptyHomeAndEnv(t *testing.T) {
	// Save and clear env. We can't reliably clear $HOME on all OSes
	// (homedir.HomeDir() also consults USERPROFILE on Windows, but we're
	// CI-Linux/macOS-only); on those platforms unset of HOME is enough.
	t.Setenv("KUBECONFIG", "")
	t.Setenv("HOME", "")
	// Some implementations of homedir consult these as fallbacks; clear
	// them too to be safe. Skip the test if a fallback is still active.
	t.Setenv("USERPROFILE", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	raw := json.RawMessage(`{"Auth":{"Type":"Kubeconfig"}}`)
	cfg, err := config.FromTargetConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.ToK8sConfig()
	if err == nil {
		t.Skip("test environment exposes a fallback HOME we can't clear; skipping")
	}
	if !strings.Contains(err.Error(), "no kubeconfig") {
		t.Errorf("expected explicit no-kubeconfig error, got %q", err.Error())
	}
}

// TestCacheKey_DistinguishesClusters ensures CacheKey composition includes
// every field that uniquely identifies a cluster — otherwise the transport
// cache aliases distinct targets.
func TestCacheKey_DistinguishesClusters(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{
			"EKS by cluster name",
			`{"Auth":{"Type":"EKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ClusterName":"a","Region":"us-east-1"}}`,
			`{"Auth":{"Type":"EKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ClusterName":"b","Region":"us-east-1"}}`,
		},
		{
			"GKE by project",
			`{"Auth":{"Type":"GKE","Endpoint":"https://e","CertificateAuthority":"Y2E=","ProjectId":"p1","Location":"l","ClusterName":"c"}}`,
			`{"Auth":{"Type":"GKE","Endpoint":"https://e","CertificateAuthority":"Y2E=","ProjectId":"p2","Location":"l","ClusterName":"c"}}`,
		},
		{
			"GKE by location",
			`{"Auth":{"Type":"GKE","Endpoint":"https://e","CertificateAuthority":"Y2E=","ProjectId":"p","Location":"us-central1","ClusterName":"c"}}`,
			`{"Auth":{"Type":"GKE","Endpoint":"https://e","CertificateAuthority":"Y2E=","ProjectId":"p","Location":"us-east1","ClusterName":"c"}}`,
		},
		{
			"AKS by resource group",
			`{"Auth":{"Type":"AKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ResourceGroup":"rg1","ClusterName":"c"}}`,
			`{"Auth":{"Type":"AKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ResourceGroup":"rg2","ClusterName":"c"}}`,
		},
		{
			"AKS by scope override",
			`{"Auth":{"Type":"AKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ResourceGroup":"rg","ClusterName":"c","Scope":"a"}}`,
			`{"Auth":{"Type":"AKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ResourceGroup":"rg","ClusterName":"c","Scope":"b"}}`,
		},
		{
			"OVH by service",
			`{"Auth":{"Type":"OVH","Endpoint":"https://e","CertificateAuthority":"Y2E=","ServiceName":"s1","ClusterId":"c"}}`,
			`{"Auth":{"Type":"OVH","Endpoint":"https://e","CertificateAuthority":"Y2E=","ServiceName":"s2","ClusterId":"c"}}`,
		},
		{
			"OCI by region",
			`{"Auth":{"Type":"OCI","Endpoint":"https://e","CertificateAuthority":"Y2E=","ClusterOcid":"c","Region":"us-chicago-1"}}`,
			`{"Auth":{"Type":"OCI","Endpoint":"https://e","CertificateAuthority":"Y2E=","ClusterOcid":"c","Region":"us-phoenix-1"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgA, err := config.FromTargetConfig([]byte(tc.a))
			if err != nil {
				t.Fatalf("parse a: %v", err)
			}
			cfgB, err := config.FromTargetConfig([]byte(tc.b))
			if err != nil {
				t.Fatalf("parse b: %v", err)
			}
			kA, err := cfgA.CacheKey()
			if err != nil {
				t.Fatalf("CacheKey a: %v", err)
			}
			kB, err := cfgB.CacheKey()
			if err != nil {
				t.Fatalf("CacheKey b: %v", err)
			}
			if kA == kB {
				t.Errorf("CacheKey collision on differing identity: %q == %q", kA, kB)
			}
		})
	}
}

func TestCacheKey_StableForEqualConfigs(t *testing.T) {
	raw := []byte(`{"Auth":{"Type":"EKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ClusterName":"c","Region":"r"}}`)
	cfgA, _ := config.FromTargetConfig(raw)
	cfgB, _ := config.FromTargetConfig(raw)
	kA, _ := cfgA.CacheKey()
	kB, _ := cfgB.CacheKey()
	if kA != kB {
		t.Errorf("equal configs produced different keys: %q vs %q", kA, kB)
	}
}

func TestCacheKey_UnsupportedAuth(t *testing.T) {
	cfg, err := config.FromTargetConfig([]byte(`{"Auth":{"Type":"NOPE"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.CacheKey(); err == nil {
		t.Error("expected error for unsupported auth type")
	}
}

