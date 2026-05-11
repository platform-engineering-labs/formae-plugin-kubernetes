// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package ovh_test

import (
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/ovh"
	"k8s.io/client-go/rest"
)

func TestOVHProvider_CarriesClusterIdentity(t *testing.T) {
	p := ovh.NewProvider("svc-1", "cluster-uuid")
	if p.ServiceName != "svc-1" {
		t.Errorf("ServiceName = %q", p.ServiceName)
	}
	if p.ClusterID != "cluster-uuid" {
		t.Errorf("ClusterID = %q", p.ClusterID)
	}
}

func TestOVHProvider_ConfigureTransport_SetsWrapTransport(t *testing.T) {
	p := ovh.NewProvider("svc", "cl")
	cfg := &rest.Config{}
	if err := p.ConfigureTransport(cfg); err != nil {
		t.Fatalf("ConfigureTransport: %v", err)
	}
	if cfg.WrapTransport == nil {
		t.Fatal("expected WrapTransport to be set")
	}
}
