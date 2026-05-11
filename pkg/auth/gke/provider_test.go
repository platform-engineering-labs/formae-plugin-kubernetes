// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package gke_test

import (
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/gke"
	"k8s.io/client-go/rest"
)

func TestGKEProvider_CarriesClusterIdentity(t *testing.T) {
	p := gke.NewProvider("my-project", "us-central1", "prod-cluster")
	if p.ProjectID != "my-project" {
		t.Errorf("ProjectID = %q", p.ProjectID)
	}
	if p.Location != "us-central1" {
		t.Errorf("Location = %q", p.Location)
	}
	if p.ClusterName != "prod-cluster" {
		t.Errorf("ClusterName = %q", p.ClusterName)
	}
}

func TestGKEProvider_ConfigureTransport_SetsWrapTransport(t *testing.T) {
	p := gke.NewProvider("proj", "loc", "name")
	cfg := &rest.Config{}
	if err := p.ConfigureTransport(cfg); err != nil {
		t.Fatalf("ConfigureTransport: %v", err)
	}
	if cfg.WrapTransport == nil {
		t.Fatal("expected WrapTransport to be set")
	}
}
