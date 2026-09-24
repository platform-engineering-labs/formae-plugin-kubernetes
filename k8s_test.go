//go:build unit

// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findFilter returns the first MatchFilter that applies to resourceType and
// whose first condition's PropertyPath contains pathSubstring. The substring
// match lets tests assert intent (e.g. "a filter on ownerReferences for Pods
// exists") without hard-coding exact JSONPath syntax, which may evolve.
func findFilter(t *testing.T, filters []model.MatchFilter, resourceType, pathSubstring string) model.MatchFilter {
	t.Helper()
	for _, f := range filters {
		if len(f.Conditions) == 0 {
			continue
		}
		hasType := false
		for _, rt := range f.ResourceTypes {
			if rt == resourceType {
				hasType = true
				break
			}
		}
		if !hasType {
			continue
		}
		if pathSubstring == "" || strings.Contains(f.Conditions[0].PropertyPath, pathSubstring) {
			return f
		}
	}
	t.Fatalf("no filter found for resourceType=%q containing path substring %q", resourceType, pathSubstring)
	return model.MatchFilter{}
}

func TestDiscoveryFilters_CronJobOwnedJobs(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Batch::Job", "ownerReferences")
	require.Len(t, f.Conditions, 1, "filter should have exactly one condition")
	assert.Contains(t, f.Conditions[0].PropertyPath, "CronJob",
		"condition should match owner references of kind CronJob")
}

func TestDiscoveryFilters_OwnedReplicaSets(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Apps::ReplicaSet", "ownerReferences")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "ownerReferences")
}

func TestDiscoveryFilters_OwnedPods(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Core::Pod", "ownerReferences")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "ownerReferences")
}

// Endpoints are filtered by the endpoints controller's own label, not by
// ownerReferences — an Endpoints object never has any, so the ownerReferences
// filter this test used to assert could never fire. See
// k8s_discovery_filter_test.go, which evaluates the filters against real object
// JSON rather than only checking their shape.
func TestDiscoveryFilters_ControllerManagedEndpoints(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Core::Endpoints", "managed-by")
	require.Len(t, f.Conditions, 1)
	assert.Equal(t, "$.metadata.labels['endpoints.kubernetes.io/managed-by']",
		f.Conditions[0].PropertyPath)
	assert.Equal(t, "endpoint-controller", f.Conditions[0].PropertyValue)
}

func TestDiscoveryFilters_ServiceAccountTokenSecrets(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Core::Secret", "type")
	require.Len(t, f.Conditions, 1)
	assert.Equal(t, "$.type", f.Conditions[0].PropertyPath)
	assert.Equal(t, "kubernetes.io/service-account-token", f.Conditions[0].PropertyValue)
}

func TestDiscoveryFilters_KubeSystemLeases(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Coordination::Lease", "namespace")
	require.Len(t, f.Conditions, 1)
	assert.Equal(t, "$.metadata.namespace", f.Conditions[0].PropertyPath)
	assert.Equal(t, "kube-system", f.Conditions[0].PropertyValue)
}

func TestDiscoveryFilters_SystemFlowSchemas(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Flowcontrol::FlowSchema", "system-")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "system-")
}

func TestDiscoveryFilters_SystemPriorityLevelConfigurations(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Flowcontrol::PriorityLevelConfiguration", "system-")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "system-")
}

func TestDiscoveryFilters_SystemPriorityClasses(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Scheduling::PriorityClass", "system-")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "system-")
}

// hasNameFilter reports whether the filter set contains at least one
// MatchFilter whose ResourceTypes include resourceType and whose first
// condition matches metadata.name == name.
func hasNameFilter(filters []model.MatchFilter, resourceType, name string) bool {
	for _, f := range filters {
		hasType := false
		for _, rt := range f.ResourceTypes {
			if rt == resourceType {
				hasType = true
				break
			}
		}
		if !hasType || len(f.Conditions) == 0 {
			continue
		}
		c := f.Conditions[0]
		if c.PropertyPath == "$.metadata.name" && c.PropertyValue == name {
			return true
		}
	}
	return false
}

// hasMetadataSearchFilter reports whether the filter set contains a
// MatchFilter on resourceType using a JSONPath search() expression that
// contains the given pattern substring (e.g. "^system-", "^eks-").
func hasMetadataSearchFilter(filters []model.MatchFilter, resourceType, patternSubstring string) bool {
	for _, f := range filters {
		hasType := false
		for _, rt := range f.ResourceTypes {
			if rt == resourceType {
				hasType = true
				break
			}
		}
		if !hasType || len(f.Conditions) == 0 {
			continue
		}
		p := f.Conditions[0].PropertyPath
		if strings.Contains(p, "search(") && strings.Contains(p, patternSubstring) {
			return true
		}
	}
	return false
}

// TestDiscoveryFilters_BuiltIns asserts that the plugin-level
// DiscoveryFilters() excludes the system-installed cluster resources
// called out in the pre-release review (H-DSC-1): system PriorityClasses,
// the bootstrap FlowSchemas / PriorityLevelConfigurations, the cloud-
// provider default StorageClasses, the cloud-provider admission webhooks,
// and the system: RBAC ClusterRoles/ClusterRoleBindings (moved here from
// List-level filtering in pkg/resources/rbac/).
func TestDiscoveryFilters_BuiltIns(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	t.Run("PriorityClass", func(t *testing.T) {
		assert.True(t, hasMetadataSearchFilter(filters, "K8S::Scheduling::PriorityClass", "^system-"),
			"PriorityClass should be filtered by system- prefix")
	})

	t.Run("FlowSchema system- prefix", func(t *testing.T) {
		assert.True(t, hasMetadataSearchFilter(filters, "K8S::Flowcontrol::FlowSchema", "^system-"))
	})

	t.Run("FlowSchema bootstrap names", func(t *testing.T) {
		// All of these ship with kube-apiserver and are managed by the control plane.
		for _, name := range []string{
			"exempt",
			"global-default",
			"catch-all",
			"probes",
			"service-accounts",
			"kube-controller-manager",
			"kube-scheduler",
			"endpoint-controller",
			"workload-high",
			"workload-low",
		} {
			assert.True(t, hasNameFilter(filters, "K8S::Flowcontrol::FlowSchema", name),
				"FlowSchema %q should be filtered", name)
		}
	})

	t.Run("PriorityLevelConfiguration system- prefix", func(t *testing.T) {
		assert.True(t, hasMetadataSearchFilter(filters, "K8S::Flowcontrol::PriorityLevelConfiguration", "^system-"))
	})

	t.Run("PriorityLevelConfiguration bootstrap names", func(t *testing.T) {
		for _, name := range []string{
			"catch-all",
			"exempt",
			"workload-high",
			"workload-low",
			"node-high",
			"leader-election",
			"global-default",
		} {
			assert.True(t, hasNameFilter(filters, "K8S::Flowcontrol::PriorityLevelConfiguration", name),
				"PriorityLevelConfiguration %q should be filtered", name)
		}
	})

	t.Run("StorageClass cloud defaults", func(t *testing.T) {
		// One representative name per managed K8s distribution.
		for _, name := range []string{
			"gp2",          // EKS in-tree
			"gp3",          // EKS EBS CSI
			"standard",     // GKE
			"standard-rwo", // GKE regional
			"premium-rwo",  // GKE premium
			"default",      // AKS
			"managed-premium",
			"local-path", // KinD / k3s
			"orbstack",   // OrbStack
		} {
			assert.True(t, hasNameFilter(filters, "K8S::Storage::StorageClass", name),
				"StorageClass %q should be filtered", name)
		}
	})

	t.Run("Admission webhook configs cloud-provider prefixes", func(t *testing.T) {
		for _, rt := range []string{
			"K8S::Admissionregistration::MutatingWebhookConfiguration",
			"K8S::Admissionregistration::ValidatingWebhookConfiguration",
		} {
			for _, prefix := range []string{"^eks-", "^gke-", "^aks-"} {
				assert.True(t, hasMetadataSearchFilter(filters, rt, prefix),
					"%s should be filtered by %q", rt, prefix)
			}
		}
	})

	t.Run("RBAC system: ClusterRoles + ClusterRoleBindings", func(t *testing.T) {
		// Moved from List-level filtering in pkg/resources/rbac/*.go into
		// plugin-level DiscoveryFilters for consistency with the other
		// system-resource exclusions. See H-DSC-1.
		assert.True(t, hasMetadataSearchFilter(filters, "K8S::Rbac::ClusterRole", "^system:"),
			"ClusterRole should be filtered by ^system: prefix at plugin level")
		assert.True(t, hasMetadataSearchFilter(filters, "K8S::Rbac::ClusterRoleBinding", "^system:"),
			"ClusterRoleBinding should be filtered by ^system: prefix at plugin level")
	})
}

//nolint:gocritic // table-free assertion is clearer for a single filter
func TestDiscoveryFilters_ExcludesHelmReleaseStorage(t *testing.T) {
	// Every revision of every Helm release is a Secret of this type, so one
	// release at the default MaxHistory would surface ten unmanaged Secrets.
	// The K8S::Helm::Release inventory collapse cannot hide them: it hides
	// objects a chart *renders*, and a release Secret appears in no manifest.
	var found bool
	for _, f := range (&Plugin{}).DiscoveryFilters() {
		for _, rt := range f.ResourceTypes {
			if rt != "K8S::Core::Secret" {
				continue
			}
			for _, c := range f.Conditions {
				if c.PropertyPath == "$.type" && c.PropertyValue == "helm.sh/release.v1" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("no DiscoveryFilter excludes Secrets of type helm.sh/release.v1")
	}
}

// TestConfigureAppliesCRDEstablishTimeout covers the PLA-711 wiring end of the
// Go side: the SDK hands the plugin the extra fields from its formae.conf.pkl
// entry, and the custom-resource establish wait must pick them up.
func TestConfigureAppliesCRDEstablishTimeout(t *testing.T) {
	p := &Plugin{}
	require.Equal(t, config.DefaultCRDEstablishTimeout, config.CRDEstablishTimeout())

	require.NoError(t, p.Configure([]byte(`{"crdEstablishTimeoutSeconds":600}`)))
	t.Cleanup(func() { _ = p.Configure([]byte(`{"crdEstablishTimeoutSeconds":0}`)) })
	assert.Equal(t, 10*time.Minute, config.CRDEstablishTimeout())
}

func TestPluginAuthPolicyIsInstanceOwnedAndPrecedesAmbientAKS(t *testing.T) {
	restricted := &Plugin{}
	unrestricted := &Plugin{}
	require.NoError(t, restricted.Configure([]byte(`{"allowedAuthMethods":["Kubeconfig"]}`)))
	require.NoError(t, unrestricted.Configure(nil))

	// If policy enforcement regresses below AKS construction, these values are
	// sufficient for DefaultAzureCredential to select its environment branch
	// and attempt Entra/ARM network access. The operation must fail on policy
	// before any of that ambient behavior is reachable.
	t.Setenv("AZURE_TENANT_ID", "11111111-1111-4111-8111-111111111111")
	t.Setenv("AZURE_CLIENT_ID", "22222222-2222-4222-8222-222222222222")
	t.Setenv("AZURE_CLIENT_SECRET", "poison-must-not-be-read")

	target := []byte(`{"Auth":{"Type":"AKS","SubscriptionId":"sub","ResourceGroup":"rg","ClusterName":"cluster"}}`)
	_, _, err := restricted.getProvisioner(context.Background(), "K8S::Core::Namespace", target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AKS:DefaultChain")
	assert.Contains(t, err.Error(), "operator policy")

	// A separate plugin instance retains the empty, unrestricted policy. Check
	// the copied dependency directly through parsing so this half never reaches
	// ambient AKS either.
	cfg, err := unrestricted.parseTargetConfig(target)
	require.NoError(t, err)
	require.NoError(t, cfg.ValidateAuthPolicy(nil))
}

func TestGetProvisionerRejectsMissingOidcSourceDespiteLegacyKeyCollision(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cache-guard.test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, privateKey.Public(), privateKey)
	require.NoError(t, err)
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	legacyRaw := fmt.Sprintf(`{"Auth":{"Type":"EKS","Endpoint":"https://cache-guard.invalid","CertificateAuthority":%q,"ClusterName":"cluster","Region":"us-east-1"}}`, ca)
	legacy, err := config.FromTargetConfig([]byte(legacyRaw))
	require.NoError(t, err)
	_, err = transport.NewClient(context.Background(), legacy)
	require.NoError(t, err, "legacy auth remains independently constructible")

	explicitRaw := fmt.Sprintf(`{"Auth":{"Type":"EKS","Endpoint":"https://cache-guard.invalid","CertificateAuthority":%q,"ClusterName":"cluster","Region":"us-east-1","Credentials":{"Type":"Oidc","RoleArn":"arn:aws:iam::123456789012:role/formae-kubernetes"}}}`, ca)
	explicit, err := config.FromTargetConfig([]byte(explicitRaw))
	require.NoError(t, err)
	legacyKey, err := legacy.ClusterKey()
	require.NoError(t, err)
	explicitKey, err := explicit.ClusterKey()
	require.NoError(t, err)
	require.Equal(t, legacyKey, explicitKey, "regression setup requires the shared physical cluster identity")

	p := &Plugin{}
	require.NoError(t, p.Configure([]byte(`{"allowedAuthMethods":["EKS:Oidc"]}`)))
	_, _, err = p.getProvisioner(context.Background(), "K8S::Core::Namespace", []byte(explicitRaw))
	require.Error(t, err)
	assert.ErrorIs(t, err, plugin.ErrNoOidcBroker)
}
