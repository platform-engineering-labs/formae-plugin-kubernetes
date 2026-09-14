// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package config_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
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

// resEnvelope renders a formae reference envelope as it reaches a plugin
// when resolution FAILED: structurally intact, no $value. formae replaces
// every reference it resolves with the scalar value before the config is
// handed over, so this shape never carries data the plugin should use.
func resEnvelope(property string) string {
	return `{"$res":true,"$label":"cluster","$type":"AWS::EKS::Cluster",` +
		`"$stack":"other-stack","$property":"` + property + `","$visibility":"Clear"}`
}

// TestFromTargetConfig_RejectsUnflattenedReference covers the cross-stack
// connect case. A reference the agent could not resolve must be rejected by
// name at parse time, not silently read as an empty string — an empty
// cluster name mints a token the API server answers with a bare 401.
func TestFromTargetConfig_RejectsUnflattenedReference(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		field string
	}{
		{
			"EKS cluster name",
			`{"Auth":{"Type":"EKS","Endpoint":"https://e","CertificateAuthority":"Y2E=","ClusterName":` +
				resEnvelope("Name") + `,"Region":"eu-central-1"}}`,
			"ClusterName",
		},
		{
			"EKS endpoint",
			`{"Auth":{"Type":"EKS","Endpoint":` + resEnvelope("Endpoint") +
				`,"CertificateAuthority":"Y2E=","ClusterName":"c","Region":"eu-central-1"}}`,
			"Endpoint",
		},
		{
			"GKE certificate authority",
			`{"Auth":{"Type":"GKE","Endpoint":"10.0.0.1","CertificateAuthority":` +
				resEnvelope("masterAuth.clusterCaCertificate") + `}}`,
			"CertificateAuthority",
		},
		{
			"AKS resource group",
			`{"Auth":{"Type":"AKS","Endpoint":"x.azmk8s.io","CertificateAuthority":"Y2E=","ResourceGroup":` +
				resEnvelope("name") + `,"ClusterName":"c"}}`,
			"ResourceGroup",
		},
		{
			"OVH service name",
			`{"Auth":{"Type":"OVH","Endpoint":"https://e","CertificateAuthority":"Y2E=","ServiceName":` +
				resEnvelope("ServiceName") + `,"ClusterId":"c"}}`,
			"ServiceName",
		},
		{
			"OCI cluster ocid",
			`{"Auth":{"Type":"OCI","Endpoint":"https://e","CertificateAuthority":"Y2E=","ClusterOcid":` +
				resEnvelope("Id") + `,"Region":"eu-frankfurt-1"}}`,
			"ClusterOcid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.FromTargetConfig([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected an error for an unresolved reference, got none")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error should name the offending field %q, got %q", tc.field, err.Error())
			}
			if !strings.Contains(err.Error(), "did not resolve") {
				t.Errorf("error should say the reference did not resolve, got %q", err.Error())
			}
		})
	}
}

// TestFromTargetConfig_NamesEveryUnflattenedReference: one apply should not
// need six round trips to find six broken references.
func TestFromTargetConfig_NamesEveryUnflattenedReference(t *testing.T) {
	raw := `{"Auth":{"Type":"AKS","Endpoint":` + resEnvelope("fqdn") +
		`,"CertificateAuthority":` + resEnvelope("certificateAuthority") +
		`,"ResourceGroup":"rg","ClusterName":"c"}}`
	_, err := config.FromTargetConfig([]byte(raw))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, field := range []string{"CertificateAuthority", "Endpoint"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error should name %q, got %q", field, err.Error())
		}
	}
}

// TestFromTargetConfig_AllowsPlainScalars guards the guard: a config whose
// references formae DID resolve is ordinary JSON and must pass untouched.
func TestFromTargetConfig_AllowsPlainScalars(t *testing.T) {
	raw := `{"Auth":{"Type":"EKS","Endpoint":"https://e.eks.amazonaws.com",` +
		`"CertificateAuthority":"Y2E=","ClusterName":"my-cluster","Region":"eu-central-1"}}`
	cfg, err := config.FromTargetConfig([]byte(raw))
	if err != nil {
		t.Fatalf("resolved config must parse: %v", err)
	}
	restCfg, err := cfg.ToK8sConfig()
	if err != nil {
		t.Fatalf("ToK8sConfig: %v", err)
	}
	if restCfg.Host != "https://e.eks.amazonaws.com" {
		t.Errorf("Host = %q, want the endpoint", restCfg.Host)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestEKSTokenFromTargetConfig drives the whole EKS path a CRUD call takes:
// target config to rest.Config to WrapTransport to a signed request. Static
// dummy credentials, no network — PresignHTTP signs locally. It pins the two
// things an EKS token is wrong without: the region-scoped STS host, and
// x-k8s-aws-id among the signed headers. Region is deliberately omitted from
// the config so the endpoint-derived path is the one under test.
func TestEKSTokenFromTargetConfig(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	t.Setenv("AWS_SESSION_TOKEN", "")

	raw := `{"Auth":{"Type":"EKS","Endpoint":"https://ABC123.gr7.eu-central-1.eks.amazonaws.com",` +
		`"CertificateAuthority":"Y2E=","ClusterName":"connect-matrix-eks"}}`
	cfg, err := config.FromTargetConfig([]byte(raw))
	if err != nil {
		t.Fatalf("FromTargetConfig: %v", err)
	}
	restCfg, err := cfg.ToK8sConfig()
	if err != nil {
		t.Fatalf("ToK8sConfig: %v", err)
	}
	if restCfg.WrapTransport == nil {
		t.Fatal("WrapTransport not set: no EKS token would be attached")
	}

	var authorization string
	rt := restCfg.WrapTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		authorization = r.Header.Get("Authorization")
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	}))
	req, err := http.NewRequest("GET", restCfg.Host+"/api/v1/namespaces", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if !strings.HasPrefix(authorization, "Bearer k8s-aws-v1.") {
		t.Fatalf("Authorization = %q, want an EKS bearer token", authorization)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(authorization, "Bearer k8s-aws-v1."))
	if err != nil {
		t.Fatalf("token payload is not base64url: %v", err)
	}
	u, err := url.Parse(string(decoded))
	if err != nil {
		t.Fatalf("token payload is not a URL: %v", err)
	}
	if u.Host != "sts.eu-central-1.amazonaws.com" {
		t.Errorf("STS host = %q, want the region derived from the endpoint", u.Host)
	}
	if !strings.Contains(u.Query().Get("X-Amz-SignedHeaders"), "x-k8s-aws-id") {
		t.Errorf("x-k8s-aws-id must be signed, got headers %q", u.Query().Get("X-Amz-SignedHeaders"))
	}
	if !strings.Contains(u.Query().Get("X-Amz-Credential"), "eu-central-1") {
		t.Errorf("credential scope missing the region: %q", u.Query().Get("X-Amz-Credential"))
	}
}

// TestAKSBareFqdnResolvesToHTTPS: Azure gives us `fqdn`, a bare hostname with
// no scheme, and the plugin assigns Endpoint to rest.Config.Host verbatim.
// client-go supplies https only because a CA bundle is set, so this pins that
// dependency rather than leaving it to chance.
func TestAKSBareFqdnResolvesToHTTPS(t *testing.T) {
	raw := `{"Auth":{"Type":"AKS","Endpoint":"cm-aks-abc123.hcp.westeurope.azmk8s.io",` +
		`"CertificateAuthority":"Y2E=","ResourceGroup":"cm-rg","ClusterName":"cm-aks"}}`
	cfg, err := config.FromTargetConfig([]byte(raw))
	if err != nil {
		t.Fatalf("FromTargetConfig: %v", err)
	}
	restCfg, err := cfg.ToK8sConfig()
	if err != nil {
		t.Fatalf("ToK8sConfig: %v", err)
	}
	if restCfg.TLSClientConfig.CAData == nil {
		t.Fatal("CAData must be set, or the scheme below silently becomes http")
	}
	base, _, err := rest.DefaultServerURL(restCfg.Host, "", schema.GroupVersion{Version: "v1"}, true)
	if err != nil {
		t.Fatalf("DefaultServerURL: %v", err)
	}
	if base.Scheme != "https" {
		t.Errorf("resolved base URL = %s, want an https scheme", base.String())
	}
	if restCfg.WrapTransport == nil {
		t.Fatal("WrapTransport not set: no Azure token would be attached")
	}
}

// TestMissingRequiredAuthFields: an omitted identifier must be named, not
// carried through as an empty string into a signed request.
func TestMissingRequiredAuthFields(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		field string
	}{
		{
			"EKS without cluster name",
			`{"Auth":{"Type":"EKS","Endpoint":"https://ABC.gr7.eu-central-1.eks.amazonaws.com","CertificateAuthority":"Y2E="}}`,
			"ClusterName",
		},
		{
			"GKE without certificate authority",
			`{"Auth":{"Type":"GKE","Endpoint":"10.0.0.1"}}`,
			"CertificateAuthority",
		},
		{
			"AKS without endpoint",
			`{"Auth":{"Type":"AKS","CertificateAuthority":"Y2E=","ResourceGroup":"rg","ClusterName":"c"}}`,
			"Endpoint",
		},
		{
			"OVH without service name",
			`{"Auth":{"Type":"OVH","Endpoint":"https://e","CertificateAuthority":"Y2E=","ClusterId":"c"}}`,
			"ServiceName",
		},
		{
			"OCI without cluster ocid",
			`{"Auth":{"Type":"OCI","Endpoint":"https://e","CertificateAuthority":"Y2E="}}`,
			"ClusterOcid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.FromTargetConfig([]byte(tc.raw))
			if err != nil {
				t.Fatalf("FromTargetConfig: %v", err)
			}
			_, err = cfg.ToK8sConfig()
			if err == nil {
				t.Fatal("expected an error for the missing field, got none")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error should name %q, got %q", tc.field, err.Error())
			}
		})
	}
}
