// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Evaluate the shipped entries rather than parallel hand-written auth snippets.
// Every entry fetched alone through MCP must carry its essential prerequisites.
func TestOIDCExamples(t *testing.T) {
	if _, err := exec.LookPath("pkl"); err != nil {
		t.Skip("pkl is not installed")
	}
	for _, mode := range []string{"eks", "aks", "gke", "direct"} {
		dir := filepath.Join("examples", "oidc-"+mode)
		out, err := exec.Command("pkl", "project", "resolve", dir).CombinedOutput()
		if err != nil {
			t.Fatalf("resolve: %v: %s", err, out)
		}
		entries, err := filepath.Glob(filepath.Join(dir, "*.pkl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			t.Run(mode+"/"+filepath.Base(entry), func(t *testing.T) {
				body, err := os.ReadFile(entry)
				if err != nil {
					t.Fatal(err)
				}
				for _, required := range []string{"k8s@0.1.13", "formae@0.90.2", "PERSISTED subject", "no parent", "separate", "JWKS", "broker", "createOnly", "ACROSS STACKS", "Helm gate", "kube-system", "ClusterRoleBinding"} {
					if !strings.Contains(string(body), required) {
						t.Errorf("Pkl-only example lacks %q", required)
					}
				}
				for _, obsolete := range []string{"unreleased example", "RELEASE_VERSION", "AFTER a compatible release is published"} {
					if strings.Contains(string(body), obsolete) {
						t.Errorf("Pkl-only example still contains pre-release instruction %q", obsolete)
					}
				}
				raw, err := exec.Command("pkl", "eval", "--project-dir", dir, "--format", "json", entry).CombinedOutput()
				if err != nil {
					t.Fatalf("eval: %v: %s", err, raw)
				}
				var forma struct {
					Targets []struct {
						Namespace    string
						Config       map[string]any
						ConfigSchema struct {
							Hints map[string]struct{ CreateOnly bool }
						}
					}
					Resources []any
				}
				if err = json.Unmarshal(raw, &forma); err != nil {
					t.Fatal(err)
				}
				if len(forma.Targets) != 1 || forma.Targets[0].Namespace != "K8S" || len(forma.Resources) != 2 {
					t.Fatalf("expected ordinary target plus namespace/workload: %s", raw)
				}
				target := forma.Targets[0]
				if !target.ConfigSchema.Hints["Auth"].CreateOnly {
					t.Fatal("Auth lost createOnly hint")
				}
				auth := target.Config["Auth"].(map[string]any)
				want := map[string]string{"eks": "EKS", "aks": "AKS", "gke": "GKE", "direct": "Oidc"}[mode]
				if auth["Type"] != want {
					t.Fatalf("Type=%v", auth["Type"])
				}
				if mode != "direct" {
					if auth["Credentials"].(map[string]any)["Type"] != "Oidc" {
						t.Fatal("missing nested Oidc credentials")
					}
				}
				if filepath.Base(entry) == "staged.pkl" {
					provider := map[string]string{"eks": "aws", "gke": "gcp"}[mode]
					pin, err := exec.Command("pkl", "eval", "--project-dir", dir, "-x", fmt.Sprintf("dependencies[%q].uri", provider), filepath.Join(dir, "PklProject")).CombinedOutput()
					if err != nil {
						t.Fatalf("evaluate provider dependency: %v: %s", err, pin)
					}
					inline := fmt.Sprintf("[\"%s\"] { uri = %q }", provider, strings.TrimSpace(string(pin)))
					if !strings.Contains(string(body), inline) {
						t.Errorf("Pkl-only staged entry lacks actual project dependency %s", inline)
					}
					ca := auth["CertificateAuthority"].(map[string]any)
					endpoint := auth["Endpoint"].(map[string]any)
					expectedCA, expectedEndpoint := "CertificateAuthorityData", "Endpoint"
					if mode == "gke" {
						expectedCA = "masterAuth.clusterCaCertificate"
						expectedEndpoint = "endpoint"
					}
					if ca["$property"] != expectedCA || endpoint["$property"] != expectedEndpoint || ca["$stack"] != "cloud-cluster" {
						t.Fatalf("incorrect actual resolvable paths: %v", auth)
					}
				}
				var check func(any)
				check = func(value any) {
					switch v := value.(type) {
					case map[string]any:
						for k, child := range v {
							if strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "secret") {
								t.Errorf("runtime secret field %s", k)
							}
							if k != "" && k[0] >= 'a' && k[0] <= 'z' {
								t.Errorf("non-PascalCase config field %s", k)
							}
							check(child)
						}
					case []any:
						for _, child := range v {
							check(child)
						}
					}
				}
				check(target.Config)
			})
		}
	}
}
