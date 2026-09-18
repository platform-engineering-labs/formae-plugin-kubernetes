// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package config_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"os"
	"strings"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
)

type kubernetesAudienceVector struct {
	Name     string `json:"name"`
	Audience string `json:"audience"`
	Valid    bool   `json:"valid"`
}

func TestDirectAuthKubernetesAudienceVectors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/kubernetes-audience-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []kubernetesAudienceVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			auth := validExplicitAuth("Oidc")
			auth["Audience"] = vector.Audience
			_, err := config.FromTargetConfig(targetJSON(t, auth))
			if got := err == nil; got != vector.Valid {
				t.Fatalf("accepted=%v for %q, want %v (err=%v)", got, vector.Audience, vector.Valid, err)
			}
		})
	}
}

const testCertificateAuthority = "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUMvekNDQWVlZ0F3SUJBZ0lVVkFCR0NZRDJsWjRwVkxINEFpaGdQaTl0ZkFzd0RRWUpLb1pJaHZjTkFRRUwKQlFBd0R6RU5NQXNHQTFVRUF3d0VkR1Z6ZERBZUZ3MHlOakE1TVRnd056QTBNVGhhRncweU5qQTVNVGt3TnpBMApNVGhhTUE4eERUQUxCZ05WQkFNTUJIUmxjM1F3Z2dFaU1BMEdDU3FHU0liM0RRRUJBUVVBQTRJQkR3QXdnZ0VLCkFvSUJBUUN4T0drSkd1TjQxRmR4cld3YjkvY1VFQXQ1ZUZHajZ0WUpYcnVoRHlmMytTVi93NHFLQXJOU2MyTkwKQnVRYzhTQjB4MzBQVy95elZEUlZVdVV2ZEZGWHdNYlRXODJpd2lGd2pjZ0NIMmhtUGdTbEp0Q0RGUkdwMFlDRAovcFBoQ2QzektHM0VBeWMxUTI2b0c5SUx3eExEeWhQbWx4c2p5UG40Slp3ZElTVmZ4YllGdHErM21wdW1GRGs3ClBsaERoY05OZ2hZcElOdS9WdnJjVWRQM1E4dmFudzF4bzl0UVlaM09lQ1JZRE1YcjZWK3lHWTVoY3lsMTZmOGkKeHZoUEZwK21OMDdaNW1EYVdnL29lM0tHZUEwTGltVWhEL3lYWS9UTzR3TDNHbjE4MTlZd2pUTGlTVmZwWEdVcQp1UTZSczJ1SGd1QVZXUmhMQmhVV0tGZmN1M2JGQWdNQkFBR2pVekJSTUIwR0ExVWREZ1FXQkJTRzQrN0FSRGZBCkw1RE9uN295clMvUEhiRGpzekFmQmdOVkhTTUVHREFXZ0JTRzQrN0FSRGZBTDVET243b3lyUy9QSGJEanN6QVAKQmdOVkhSTUJBZjhFQlRBREFRSC9NQTBHQ1NxR1NJYjNEUUVCQ3dVQUE0SUJBUUJ1bHVFMjZPV0xQTnN1OXYvcwprL3dqeDRXZ3I5SFkxaWhBMW5FNEh1R3ErRWZZakxTR29GTnRpTmxpVnVhem45RU5Cemg3SWppcldDR0xGUjFWCnBHbS9ZRlFyTUFsUDEvQXpwa1hhTWw0akVlUUszcy9TM2hvYnJDei8rdVJhK2h0eEhNZkNEdkd5Y1pGbkRjZUQKWWdMNDV5dytoZlBydUdqSEM4L1BUZDV1a2Z4Q09wdW1MSXZlWWNuQTdQZTQ3bE9NVHlTZ0pjVlFFcXJsYkhZOApyMzB0djNTUXlhM3g3R2gzV0trdGhlN1l6R01LYmozNytrb0dDV1Fuai9LZFhIb21lcWhuY0dia1p1T2RBYXdiCkhZaTZtQVhHQzVVTksydkdDbG5MMXM1cWduamJlMzdqNVdDRjdROFIxZUVGNno1amlJYnZsSEFZTWNGK0RRRmsKWGdMagotLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tCg=="

func targetJSON(t *testing.T, auth map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"Auth": auth})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func parseExplicit(t *testing.T, authType string) *config.Config {
	t.Helper()
	cfg, err := config.FromTargetConfig(targetJSON(t, validExplicitAuth(authType)))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAuthMethodClassifiesLegacyAndExplicitModes(t *testing.T) {
	tests := []struct {
		name string
		auth map[string]any
		want string
		oidc bool
	}{
		{name: "EKS OIDC", auth: validExplicitAuth("EKS"), want: "EKS:Oidc", oidc: true},
		{name: "AKS OIDC", auth: validExplicitAuth("AKS"), want: "AKS:Oidc", oidc: true},
		{name: "GKE OIDC", auth: validExplicitAuth("GKE"), want: "GKE:Oidc", oidc: true},
		{name: "direct OIDC", auth: validExplicitAuth("Oidc"), want: "Oidc", oidc: true},
		{name: "EKS legacy", auth: map[string]any{"Type": "EKS"}, want: "EKS:DefaultChain"},
		{name: "AKS legacy", auth: map[string]any{"Type": "AKS"}, want: "AKS:DefaultChain"},
		{name: "GKE legacy", auth: map[string]any{"Type": "GKE"}, want: "GKE:ADC"},
		{name: "kubeconfig", auth: map[string]any{"Type": "Kubeconfig"}, want: "Kubeconfig"},
		{name: "OVH", auth: map[string]any{"Type": "OVH"}, want: "OVH"},
		{name: "OCI", auth: map[string]any{"Type": "OCI"}, want: "OCI"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.FromTargetConfig(targetJSON(t, tc.auth))
			if err != nil {
				t.Fatal(err)
			}
			got, err := cfg.AuthMethod()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("AuthMethod() = %q, want %q", got, tc.want)
			}
			if cfg.UsesOidc() != tc.oidc {
				t.Fatalf("UsesOidc() = %v, want %v", cfg.UsesOidc(), tc.oidc)
			}
		})
	}
}

func TestValidateAuthPolicyIsFailClosedAndTargetCannotOverrideIt(t *testing.T) {
	cfg := parseExplicit(t, "EKS")
	if err := cfg.ValidateAuthPolicy(nil); err != nil {
		t.Fatalf("empty policy must retain legacy unrestricted behavior: %v", err)
	}
	if err := cfg.ValidateAuthPolicy([]string{"EKS:Oidc"}); err != nil {
		t.Fatalf("explicitly allowed method: %v", err)
	}
	if err := cfg.ValidateAuthPolicy([]string{"Kubeconfig"}); err == nil || !strings.Contains(err.Error(), "EKS:Oidc") {
		t.Fatalf("disallowed method error = %v", err)
	}

	raw := targetJSON(t, validExplicitAuth("EKS"))
	var target map[string]any
	if err := json.Unmarshal(raw, &target); err != nil {
		t.Fatal(err)
	}
	target["AllowedAuthMethods"] = []string{"EKS:Oidc"}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = config.FromTargetConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetAuthDependencies(config.AuthDependencies{AllowedAuthMethods: []string{"Kubeconfig"}})
	if _, err := cfg.ToK8sConfig(context.Background()); err == nil || !strings.Contains(err.Error(), "EKS:Oidc") {
		t.Fatalf("target policy override bypassed operator policy: %v", err)
	}
}

func TestAuthFingerprintSeparatesEveryExplicitIdentityCoordinate(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		mutate func(map[string]any)
	}{
		{name: "CA", base: "EKS", mutate: func(a map[string]any) {
			pemBytes, _ := base64.StdEncoding.DecodeString(testCertificateAuthority)
			a["CertificateAuthority"] = base64.StdEncoding.EncodeToString(append(pemBytes, pemBytes...))
		}},
		{name: "AWS role", base: "EKS", mutate: func(a map[string]any) {
			a["Credentials"].(map[string]any)["RoleArn"] = "arn:aws:iam::123456789012:role/other-role"
		}},
		{name: "Azure client", base: "AKS", mutate: func(a map[string]any) {
			a["Credentials"].(map[string]any)["ClientId"] = "33333333-3333-4333-8333-333333333333"
		}},
		{name: "GCP provider", base: "GKE", mutate: func(a map[string]any) {
			a["Credentials"].(map[string]any)["WorkloadIdentityProvider"] = "//iam.googleapis.com/projects/987654321098/locations/global/workloadIdentityPools/formae/providers/installation"
		}},
		{name: "GCP service account", base: "GKE", mutate: func(a map[string]any) {
			a["Credentials"].(map[string]any)["ServiceAccountEmail"] = "other@example-project.iam.gserviceaccount.com"
		}},
		{name: "direct audience", base: "Oidc", mutate: func(a map[string]any) {
			a["Audience"] = "urn:formae:kubernetes:49c24d1d-3815-4817-9242-4032be46601b"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := validExplicitAuth(tc.base)
			first, err := config.FromTargetConfig(targetJSON(t, base))
			if err != nil {
				t.Fatal(err)
			}
			firstFingerprint, err := first.AuthFingerprint()
			if err != nil {
				t.Fatal(err)
			}
			identical, _ := config.FromTargetConfig(targetJSON(t, base))
			identicalFingerprint, _ := identical.AuthFingerprint()
			if firstFingerprint != identicalFingerprint {
				t.Fatalf("identical config fingerprints differ: %q != %q", firstFingerprint, identicalFingerprint)
			}

			mutated := validExplicitAuth(tc.base)
			tc.mutate(mutated)
			second, err := config.FromTargetConfig(targetJSON(t, mutated))
			if err != nil {
				t.Fatal(err)
			}
			secondFingerprint, err := second.AuthFingerprint()
			if err != nil {
				t.Fatal(err)
			}
			if firstFingerprint == secondFingerprint {
				t.Fatalf("%s change did not isolate auth identity", tc.name)
			}
			for _, coordinate := range []string{"formae-kubernetes", "example-project", "39c24d1d"} {
				if strings.Contains(firstFingerprint, coordinate) {
					t.Fatalf("fingerprint exposes raw coordinate %q: %s", coordinate, firstFingerprint)
				}
			}
		})
	}
}

func TestClusterKeyTracksPhysicalClusterOnly(t *testing.T) {
	base := validExplicitAuth("EKS")
	base["Endpoint"] = "https://KUBERNETES.EXAMPLE.COM:443/"
	first, err := config.FromTargetConfig(targetJSON(t, base))
	if err != nil {
		t.Fatal(err)
	}
	firstKey, err := first.ClusterKey()
	if err != nil {
		t.Fatal(err)
	}

	changed := validExplicitAuth("EKS")
	changed["Endpoint"] = "https://kubernetes.example.com"
	changed["Credentials"].(map[string]any)["RoleArn"] = "arn:aws:iam::123456789012:role/other-role"
	pemBytes, _ := base64.StdEncoding.DecodeString(testCertificateAuthority)
	changed["CertificateAuthority"] = base64.StdEncoding.EncodeToString(append(pemBytes, pemBytes...))
	second, err := config.FromTargetConfig(targetJSON(t, changed))
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := second.ClusterKey()
	if err != nil {
		t.Fatal(err)
	}
	if firstKey != secondKey {
		t.Fatalf("credential/CA/cosmetic endpoint change altered physical key: %q != %q", firstKey, secondKey)
	}
}

func TestClusterKeyRejectsEmptyQuerySyntax(t *testing.T) {
	cfg, err := config.FromTargetConfig([]byte(`{"Auth":{"Type":"EKS","Endpoint":"https://physical.example?","CertificateAuthority":"Y2E=","ClusterName":"cluster","Region":"us-east-1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if key, err := cfg.ClusterKey(); err == nil {
		t.Fatalf("ClusterKey accepted an empty query marker as a distinct physical key: %q", key)
	}
}

func TestClusterKeyUsesEndpoint(t *testing.T) {
	cfg, err := config.FromTargetConfig([]byte(`{"Auth":{"Type":"EKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ClusterName":"c","Region":"r"}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.ClusterKey()
	if err != nil {
		t.Fatal(err)
	}
	if got != "Endpoint|https://e" {
		t.Fatalf("ClusterKey() = %q; physical endpoint identity changed", got)
	}
}

func TestExplicitOidcCannotFallThroughToAmbientProviders(t *testing.T) {
	for _, authType := range []string{"EKS", "AKS", "GKE", "Oidc"} {
		t.Run(authType, func(t *testing.T) {
			cfg := parseExplicit(t, authType)
			cfg.SetAuthDependencies(config.AuthDependencies{AllowedAuthMethods: []string{
				"EKS:Oidc", "AKS:Oidc", "GKE:Oidc", "Oidc",
			}})
			_, err := cfg.ToK8sConfig(context.Background())
			if err == nil {
				t.Fatal("explicit OIDC accepted a missing broker source")
			}
			if !errors.Is(err, plugin.ErrNoOidcBroker) {
				t.Fatalf("unexpected missing-broker error: %v", err)
			}
		})
	}
}

func validExplicitAuth(authType string) map[string]any {
	base := map[string]any{
		"Type":                 authType,
		"Endpoint":             "https://kubernetes.example.com:6443",
		"CertificateAuthority": testCertificateAuthority,
	}
	switch authType {
	case "EKS":
		base["ClusterName"] = "production"
		base["Region"] = "us-east-1"
		base["Credentials"] = map[string]any{
			"Type":    "Oidc",
			"RoleArn": "arn:aws:iam::123456789012:role/formae-kubernetes",
		}
	case "AKS":
		base["Credentials"] = map[string]any{
			"Type":     "Oidc",
			"TenantId": "11111111-1111-4111-8111-111111111111",
			"ClientId": "22222222-2222-4222-8222-222222222222",
		}
	case "GKE":
		base["Credentials"] = map[string]any{
			"Type":                     "Oidc",
			"WorkloadIdentityProvider": "//iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/formae/providers/installation",
			"ServiceAccountEmail":      "formae-kubernetes@example-project.iam.gserviceaccount.com",
		}
	case "Oidc":
		base["Audience"] = "urn:formae:kubernetes:39c24d1d-3815-4817-9242-4032be46601b"
	}
	return base
}

// Each explicit authentication shape must be completely validated while the
// target is parsed, before any provider constructor can inspect ambient state.
func TestFromTargetConfigAcceptsFourExplicitAuthVariants(t *testing.T) {
	for _, authType := range []string{"EKS", "AKS", "GKE", "Oidc"} {
		t.Run(authType, func(t *testing.T) {
			cfg, err := config.FromTargetConfig(targetJSON(t, validExplicitAuth(authType)))
			if err != nil {
				t.Fatalf("valid %s config: %v", authType, err)
			}
			if cfg.AuthType() != authType {
				t.Fatalf("AuthType() = %q, want %q", cfg.AuthType(), authType)
			}
		})
	}
}

func TestFromTargetConfigRejectsMalformedExplicitCredentials(t *testing.T) {
	tests := []struct {
		name      string
		auth      map[string]any
		wantField string
	}{
		{
			name: "nested unresolved role",
			auth: func() map[string]any {
				a := validExplicitAuth("EKS")
				a["Credentials"].(map[string]any)["RoleArn"] = map[string]any{"$res": true, "$ref": "resource.output"}
				return a
			}(),
			wantField: "RoleArn",
		},
		{
			name: "unknown credential discriminator",
			auth: func() map[string]any {
				a := validExplicitAuth("EKS")
				a["Credentials"].(map[string]any)["Type"] = "Static"
				return a
			}(),
			wantField: "Credentials.Type",
		},
		{
			name: "provider credential fields cannot be mixed",
			auth: func() map[string]any {
				a := validExplicitAuth("EKS")
				a["Credentials"].(map[string]any)["TenantId"] = "11111111-1111-4111-8111-111111111111"
				return a
			}(),
			wantField: "TenantId",
		},
		{
			name: "profile conflicts with explicit identity",
			auth: func() map[string]any {
				a := validExplicitAuth("EKS")
				a["Profile"] = "developer"
				return a
			}(),
			wantField: "Profile",
		},
		{
			name: "commercial AWS role ARN only",
			auth: func() map[string]any {
				a := validExplicitAuth("EKS")
				a["Credentials"].(map[string]any)["RoleArn"] = "arn:aws-us-gov:iam::123456789012:role/secret-role"
				return a
			}(),
			wantField: "RoleArn",
		},
		{
			name: "explicit AWS region required",
			auth: func() map[string]any {
				a := validExplicitAuth("EKS")
				a["Region"] = ""
				return a
			}(),
			wantField: "Region",
		},
		{
			name: "AKS endpoint required",
			auth: func() map[string]any {
				a := validExplicitAuth("AKS")
				a["Endpoint"] = nil
				return a
			}(),
			wantField: "Endpoint",
		},
		{
			name: "AKS CA required",
			auth: func() map[string]any {
				a := validExplicitAuth("AKS")
				a["CertificateAuthority"] = ""
				return a
			}(),
			wantField: "CertificateAuthority",
		},
		{
			name: "Azure tenant UUID",
			auth: func() map[string]any {
				a := validExplicitAuth("AKS")
				a["Credentials"].(map[string]any)["TenantId"] = "not-a-secret-uuid"
				return a
			}(),
			wantField: "TenantId",
		},
		{
			name: "Azure fixed scope",
			auth: func() map[string]any {
				a := validExplicitAuth("AKS")
				a["Scope"] = "https://management.azure.com/.default"
				return a
			}(),
			wantField: "Scope",
		},
		{
			name: "canonical GCP provider",
			auth: func() map[string]any {
				a := validExplicitAuth("GKE")
				a["Credentials"].(map[string]any)["WorkloadIdentityProvider"] = "projects/123/providers/installation"
				return a
			}(),
			wantField: "WorkloadIdentityProvider",
		},
		{
			name: "canonical service account email",
			auth: func() map[string]any {
				a := validExplicitAuth("GKE")
				a["Credentials"].(map[string]any)["ServiceAccountEmail"] = "not-an-email"
				return a
			}(),
			wantField: "ServiceAccountEmail",
		},
		{
			name: "HTTPS origin",
			auth: func() map[string]any {
				a := validExplicitAuth("Oidc")
				a["Endpoint"] = "https://user@example.com/cluster?token=secret#fragment"
				return a
			}(),
			wantField: "Endpoint",
		},
		{
			name: "base64 PEM CA",
			auth: func() map[string]any {
				a := validExplicitAuth("Oidc")
				a["CertificateAuthority"] = "bm90IGEgcGVtIGNlcnRpZmljYXRl"
				return a
			}(),
			wantField: "CertificateAuthority",
		},
		{
			name: "HTTPS origin requires a hostname",
			auth: func() map[string]any {
				a := validExplicitAuth("Oidc")
				a["Endpoint"] = "https://:6443"
				return a
			}(),
			wantField: "Endpoint",
		},
		{
			name: "HTTPS origin rejects empty query syntax",
			auth: func() map[string]any {
				a := validExplicitAuth("Oidc")
				a["Endpoint"] = "https://kubernetes.example.com?"
				return a
			}(),
			wantField: "Endpoint",
		},
		{
			name: "canonical direct audience",
			auth: func() map[string]any {
				a := validExplicitAuth("Oidc")
				a["Audience"] = "urn:formae:kubernetes:39C24D1D-3815-4817-9242-4032BE46601B"
				return a
			}(),
			wantField: "Audience",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.FromTargetConfig(targetJSON(t, tc.auth))
			if err == nil {
				t.Fatal("expected malformed explicit config to be rejected")
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("error %q does not name %s", err, tc.wantField)
			}
			for _, secret := range []string{"secret-role", "not-a-secret-uuid", "token=secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked credential material %q: %v", secret, err)
				}
			}
		})
	}
}

func TestFromTargetConfigPreservesLegacyCredentialOmission(t *testing.T) {
	for _, tc := range []struct {
		name        string
		credentials any
		include     bool
	}{
		{name: "omitted"},
		{name: "null", credentials: nil, include: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := map[string]any{
				"Type":                 "AKS",
				"Endpoint":             "legacy.example.com",
				"CertificateAuthority": "Y2E=",
				"ResourceGroup":        "rg",
				"ClusterName":          "cluster",
			}
			if tc.include {
				auth["Credentials"] = tc.credentials
			}
			if _, err := config.FromTargetConfig(targetJSON(t, auth)); err != nil {
				t.Fatalf("legacy config must remain accepted: %v", err)
			}
		})
	}
}

func TestExplicitEKSRequiresCommercialRegion(t *testing.T) {
	for _, region := range []string{"cn-north-1", "us-gov-west-1", "us-iso-east-1", "us-isob-east-1", "eu-isoe-west-1", "eusc-de-east-1"} {
		t.Run(region, func(t *testing.T) {
			a := validExplicitAuth("EKS")
			a["Region"] = region
			if _, err := config.FromTargetConfig(targetJSON(t, a)); err == nil || !strings.Contains(err.Error(), "Region") {
				t.Fatalf("expected region validation error, got %v", err)
			}
		})
	}
	for _, region := range []string{"us-east-1", "eu-central-1", "ap-southeast-5", "il-central-1", "mx-central-1"} {
		a := validExplicitAuth("EKS")
		a["Region"] = region
		if _, err := config.FromTargetConfig(targetJSON(t, a)); err != nil {
			t.Fatalf("commercial region %s: %v", region, err)
		}
	}
}
