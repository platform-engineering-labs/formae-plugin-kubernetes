// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func evalTargetPkl(t *testing.T, body string) (map[string]any, error) {
	t.Helper()
	if _, err := exec.LookPath("pkl"); err != nil {
		t.Skip("pkl is not installed")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "schema", "pkl-main")
	if _, err := os.Stat(filepath.Join(project, "PklProject.deps.json")); os.IsNotExist(err) {
		cmd := exec.Command("pkl", "project", "resolve", project)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("resolve Pkl project: %v\n%s", err, output)
		}
	}
	source := `import "file://` + filepath.Join(project, "target.pkl") + `" as k8s
value = ` + body + "\n"
	path := filepath.Join(t.TempDir(), "auth.pkl")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("pkl", "eval", "--project-dir", project, "--format", "json", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &pklEvalError{err: err, output: string(output)}
	}
	var value map[string]any
	if err := json.Unmarshal(output, &value); err != nil {
		t.Fatalf("decode Pkl JSON: %v\n%s", err, output)
	}
	return value, nil
}

type pklEvalError struct {
	err    error
	output string
}

func (e *pklEvalError) Error() string { return e.err.Error() + ": " + e.output }

func TestTargetSchemaExplicitAuthJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "EKS",
			body: `new k8s.EKSAuth {
  endpoint = "https://eks.example.com"
  certificateAuthority = "Y2E="
  clusterName = "production"
  region = "us-east-1"
  credentials = new k8s.AwsOidcCredentials { roleArn = "arn:aws:iam::123456789012:role/formae" }
}`,
			want: map[string]any{
				"Type": "EKS", "Endpoint": "https://eks.example.com", "CertificateAuthority": "Y2E=",
				"ClusterName": "production", "Region": "us-east-1",
				"Credentials": map[string]any{"Type": "Oidc", "RoleArn": "arn:aws:iam::123456789012:role/formae"},
			},
		},
		{
			name: "AKS",
			body: `new k8s.AKSAuth {
  endpoint = "https://aks.example.com"
  certificateAuthority = "Y2E="
  credentials = new k8s.AzureOidcCredentials {
    tenantId = "11111111-1111-4111-8111-111111111111"
    clientId = "22222222-2222-4222-8222-222222222222"
  }
}`,
			want: map[string]any{
				"Type": "AKS", "Endpoint": "https://aks.example.com", "CertificateAuthority": "Y2E=",
				"Credentials": map[string]any{"Type": "Oidc", "TenantId": "11111111-1111-4111-8111-111111111111", "ClientId": "22222222-2222-4222-8222-222222222222"},
			},
		},
		{
			name: "GKE",
			body: `new k8s.GKEAuth {
  endpoint = "https://gke.example.com"
  certificateAuthority = "Y2E="
  credentials = new k8s.GcpOidcCredentials {
    workloadIdentityProvider = "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/formae/providers/install"
    serviceAccountEmail = "formae@example.iam.gserviceaccount.com"
  }
}`,
			want: map[string]any{
				"Type": "GKE", "Endpoint": "https://gke.example.com", "CertificateAuthority": "Y2E=",
				"Credentials": map[string]any{
					"Type": "Oidc", "WorkloadIdentityProvider": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/formae/providers/install",
					"ServiceAccountEmail": "formae@example.iam.gserviceaccount.com",
				},
			},
		},
		{
			name: "direct",
			body: `new k8s.OidcAuth {
  endpoint = "https://kubernetes.example.com:6443"
  certificateAuthority = "Y2E="
  audience = "urn:formae:kubernetes:39c24d1d-3815-4817-9242-4032be46601b"
}`,
			want: map[string]any{
				"Type": "Oidc", "Endpoint": "https://kubernetes.example.com:6443", "CertificateAuthority": "Y2E=",
				"Audience": "urn:formae:kubernetes:39c24d1d-3815-4817-9242-4032be46601b",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evalTargetPkl(t, tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if encoded, _ := json.Marshal(got["value"]); string(encoded) != mustJSON(t, tc.want) {
				t.Fatalf("rendered auth = %s, want %s", encoded, mustJSON(t, tc.want))
			}
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestTargetSchemaRejectsCrossProviderCredentials(t *testing.T) {
	_, err := evalTargetPkl(t, `new k8s.EKSAuth {
  endpoint = "https://eks.example.com"
  certificateAuthority = "Y2E="
  clusterName = "production"
  region = "us-east-1"
  credentials = new k8s.AzureOidcCredentials {
    tenantId = "11111111-1111-4111-8111-111111111111"
    clientId = "22222222-2222-4222-8222-222222222222"
  }
}`)
	if err == nil {
		t.Fatal("EKS accepted Azure credentials")
	}
}

func TestTargetSchemaRequiresAKSConnectionWithCredentials(t *testing.T) {
	_, err := evalTargetPkl(t, `new k8s.AKSAuth {
  credentials = new k8s.AzureOidcCredentials {
    tenantId = "11111111-1111-4111-8111-111111111111"
    clientId = "22222222-2222-4222-8222-222222222222"
  }
}`)
	if err == nil {
		t.Fatal("AKS credentials without endpoint and CA evaluated successfully")
	}
}
