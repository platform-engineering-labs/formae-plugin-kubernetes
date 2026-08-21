//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package custom_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	certManagerRepoURL = "https://charts.jetstack.io"
	certManagerVersion = "v1.13.3"
	certManagerRelease = "cert-manager"
)

var crdResource = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// certManagerCRDs lists the CRDs of cert-manager's two API groups.
func certManagerCRDs(ctx context.Context, t *testing.T, client *transport.Client) []unstructured.Unstructured {
	t.Helper()
	list, err := client.Dynamic.Resource(crdResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list CRDs: %v", err)
	}
	var out []unstructured.Unstructured
	for _, crd := range list.Items {
		if strings.HasSuffix(crd.GetName(), "cert-manager.io") {
			out = append(out, crd)
		}
	}
	return out
}

// TestCertManagerReleaseThenClusterIssuer is the field repro behind PLA-711:
// cert-manager as a K8S::Helm::Release with installCRDs, plus a ClusterIssuer
// as a K8S::Custom::Resource, in one apply. Live, the issuer was applied at
// ~0:32 and cert-manager.io/v1 only became servable at ~1:38, so the CR failed
// with "no matches for kind ClusterIssuer in version cert-manager.io/v1" — the
// establish-retry loop had already given up at its old 30s bound.
//
// Unlike the crds/ chart in crdrace_integration_test.go, cert-manager renders
// its CRDs as ordinary templates, so this covers the other half: the CRDs are
// in the release manifest but still take a minute to establish.
//
// Slow by nature (real chart, real image pulls). Skipped in -short.
func TestCertManagerReleaseThenClusterIssuer(t *testing.T) {
	if testing.Short() {
		t.Skip("installs cert-manager from the network")
	}
	env := testutil.SetupEnv(t)
	ctx := context.Background()

	// A cluster that already serves cert-manager.io cannot show the race, and
	// its CRDs are somebody's real install — never delete those.
	if existing := certManagerCRDs(ctx, t, env.Client); len(existing) > 0 {
		t.Skipf("cluster already has %d cert-manager CRD(s), e.g. %s; refusing to touch them",
			len(existing), existing[0].GetName())
	}

	rel := env.NewProvisioner(releaseType)
	cr := env.NewProvisioner(customType)

	// cert-manager stamps helm.sh/resource-policy: keep on its CRD templates, so
	// `helm uninstall` leaves all six behind — cluster-scoped, and enough to
	// make the next run of this test see a servable kind from the start.
	// Scoped to the CRDs this run's release owns, by the unique namespace.
	t.Cleanup(func() {
		ctx := context.Background()
		for _, crd := range certManagerCRDs(ctx, t, env.Client) {
			if crd.GetAnnotations()["meta.helm.sh/release-namespace"] != env.Namespace {
				t.Logf("leaving CRD %s: not installed by this run", crd.GetName())
				continue
			}
			if err := env.Client.Dynamic.Resource(crdResource).Delete(ctx, crd.GetName(), metav1.DeleteOptions{}); err != nil {
				t.Logf("warning: delete CRD %s: %v", crd.GetName(), err)
			}
		}
	})

	issuerID := prov.CustomResourceID("cert-manager.io/v1", "ClusterIssuer", "", "formae-selfsigned")
	t.Cleanup(func() {
		_, _ = cr.Delete(context.Background(), &resource.DeleteRequest{ResourceType: customType, NativeID: issuerID})
	})

	// --- The ClusterIssuer goes first, before its kind exists ---------------
	// The forma declares no edge between the two, so formae is free to start
	// them in either order; this pins the losing order.
	issuer := testutil.MustMarshalJSON(t, map[string]any{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "ClusterIssuer",
		"metadata":   map[string]any{"name": "formae-selfsigned"},
		// The empty selfSigned map is the whole issuer. Sent straight through
		// here; PLA-710 is formae core pruning it out of the opaque spec before
		// the plugin ever sees it, which is a different bug.
		"spec": map[string]any{"selfSigned": map[string]any{}},
	})
	type createResult struct {
		res *resource.CreateResult
		err error
	}
	done := make(chan createResult, 1)
	start := time.Now()
	go func() {
		res, err := cr.Create(ctx, &resource.CreateRequest{ResourceType: customType, Properties: issuer})
		done <- createResult{res, err}
	}()

	select {
	case got := <-done:
		t.Fatalf("ClusterIssuer Create returned in %s, before cert-manager was installed: err=%v", time.Since(start), got.err)
	case <-time.After(3 * time.Second):
	}

	// --- cert-manager installs, CRDs included -------------------------------
	relProps := testutil.MustMarshalJSON(t, map[string]any{
		"metadata":       map[string]any{"name": certManagerRelease, "namespace": env.Namespace},
		"chart":          "cert-manager",
		"repoURL":        certManagerRepoURL,
		"version":        certManagerVersion,
		"values":         map[string]any{"installCRDs": true},
		"timeoutSeconds": 900,
	})
	created, err := rel.Create(ctx, &resource.CreateRequest{ResourceType: releaseType, Properties: relProps})
	if err != nil {
		t.Fatalf("create cert-manager release: %v", err)
	}
	relReqID := created.ProgressResult.RequestID
	t.Cleanup(func() { uninstallAndWait(t, rel, prov.NativeID(env.Namespace, certManagerRelease)) })

	// --- The issuer converges once cert-manager.io/v1 is servable -----------
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("ClusterIssuer never converged after %s: %v", time.Since(start), got.err)
		}
		testutil.RequireSuccess(t, got.res.ProgressResult, "create ClusterIssuer")
		if got.res.ProgressResult.NativeID != issuerID {
			t.Errorf("ClusterIssuer NativeID = %q, want %q", got.res.ProgressResult.NativeID, issuerID)
		}
		t.Logf("ClusterIssuer converged after %s of waiting on the CRD", time.Since(start))
	case <-time.After(4 * time.Minute):
		t.Fatal("ClusterIssuer Create never converged; cert-manager.io/v1 stayed unservable")
	}

	// --- And the release itself finishes ------------------------------------
	final := testutil.WaitForStatus(t, rel, "", releaseType, relReqID, 10*time.Minute)
	testutil.RequireSuccess(t, final.ProgressResult, "cert-manager install")
}
