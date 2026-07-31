// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package config

import (
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/eks"
)

// The AWS credential profile declared on an EKSAuth block must reach the
// eks.Provider that mints STS tokens — otherwise token minting falls back to
// the default credential chain regardless of what the target declared.
func TestNewEKSProvider_ThreadsProfileFromConfig(t *testing.T) {
	cfg, err := FromTargetConfig([]byte(`{"Auth":{"Type":"EKS","Endpoint":"https://ABC.gr7.us-west-2.eks.amazonaws.com","CertificateAuthority":"Y2E=","ClusterName":"my-cluster","Region":"us-west-2","Profile":"blue"}}`))
	if err != nil {
		t.Fatalf("FromTargetConfig: %v", err)
	}

	prov, _, err := cfg.newEKSProvider()
	if err != nil {
		t.Fatalf("newEKSProvider: %v", err)
	}

	p, ok := prov.(*eks.Provider)
	if !ok {
		t.Fatalf("expected *eks.Provider, got %T", prov)
	}
	if p.Profile != "blue" {
		t.Errorf("Profile = %q, want %q", p.Profile, "blue")
	}
}
