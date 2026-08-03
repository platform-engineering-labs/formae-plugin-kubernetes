//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
)

const manifest = `---
# Source: app/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-web
spec:
  replicas: 1
---
apiVersion: v1
kind: Service
metadata:
  name: app-web
  namespace: explicit-ns
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
`

func TestIndexManifest_DefaultsNamespaceAndIndexesEveryDoc(t *testing.T) {
	inv := newInventory()
	indexManifest(inv, manifest, "release-ns", "release-ns/app")

	if got := inv.Len(); got != 3 {
		t.Fatalf("indexed %d objects, want 3", got)
	}

	// No explicit namespace -> the release namespace applies.
	if owner, ok := inv.OwnedBy("Deployment", "release-ns", "app-web"); !ok || owner != "release-ns/app" {
		t.Errorf("Deployment not owned by release: owner=%q ok=%v", owner, ok)
	}

	// An explicit namespace in the manifest wins over the release namespace.
	if _, ok := inv.OwnedBy("Service", "explicit-ns", "app-web"); !ok {
		t.Error("Service with explicit namespace not indexed under it")
	}
	if _, ok := inv.OwnedBy("Service", "release-ns", "app-web"); ok {
		t.Error("Service indexed under the release namespace, overriding its explicit one")
	}

	// Cluster-scoped kinds reach the filter with no namespace, so both forms
	// must resolve.
	if _, ok := inv.OwnedBy("CustomResourceDefinition", "", "widgets.example.com"); !ok {
		t.Error("cluster-scoped object not matchable without a namespace")
	}
}

func TestSplitYAMLDocs_IgnoresSeparatorInsideValues(t *testing.T) {
	// A "---" that is not a document separator must not split, or the object
	// after it is lost from the inventory and resurfaces as unmanaged.
	src := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\ndata:\n  key: \"a --- b\"\n"
	if got := len(splitYAMLDocs(src)); got != 1 {
		t.Fatalf("split into %d docs, want 1", got)
	}
}

func TestSplitYAMLDocs_SkipsEmptyDocuments(t *testing.T) {
	// Helm emits trailing separators and comment-only documents for templates
	// that render to nothing under the supplied values.
	src := "---\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n---\n"
	if got := len(splitYAMLDocs(src)); got != 1 {
		t.Fatalf("split into %d docs, want 1", got)
	}
}

func TestFilterHelmOwned_DropsOnlyChartObjects(t *testing.T) {
	inv := newInventory()
	indexManifest(inv, manifest, "release-ns", "release-ns/app")

	ids := []string{
		"release-ns/app-web",  // rendered by the chart -> hidden
		"release-ns/user-own", // hand-authored -> kept
	}
	got := FilterHelmOwned(inv, "K8S::Apps::Deployment", ids)

	if len(got) != 1 || got[0] != "release-ns/user-own" {
		t.Fatalf("filtered to %v, want [release-ns/user-own]", got)
	}
}

func TestFilterHelmOwned_KindMustMatch(t *testing.T) {
	inv := newInventory()
	indexManifest(inv, manifest, "release-ns", "release-ns/app")

	// Same namespace/name as the chart's Deployment, different kind. Filtering
	// on namespace/name alone would wrongly hide this.
	got := FilterHelmOwned(inv, "K8S::Core::ConfigMap", []string{"release-ns/app-web"})
	if len(got) != 1 {
		t.Fatalf("ConfigMap named like the chart's Deployment was hidden: %v", got)
	}
}

func TestFilterHelmOwned_PassesThroughUnknownResourceType(t *testing.T) {
	inv := newInventory()
	indexManifest(inv, manifest, "release-ns", "release-ns/app")

	// A type that is not NAMESPACE::Service::Resource yields no kind; dropping
	// everything would erase the whole list from discovery.
	got := FilterHelmOwned(inv, "NotAResourceType", []string{"release-ns/app-web"})
	if len(got) != 1 {
		t.Fatalf("unknown resource type filtered its ids: %v", got)
	}
}

func TestKindFromResourceType(t *testing.T) {
	for in, want := range map[string]string{
		"K8S::Apps::Deployment": "Deployment",
		"K8S::Core::ConfigMap":  "ConfigMap",
		"K8S::Helm::Release":    "Release",
		"too::few":              "",
		"":                      "",
	} {
		if got := KindFromResourceType(in); got != want {
			t.Errorf("KindFromResourceType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequestIDRoundTrip(t *testing.T) {
	// The RequestID is the only handle Status gets on an install: Create
	// withholds the NativeID until the release is deployed, so namespace and
	// name have to survive this round trip or Status cannot find the release.
	id := requestID("monitoring", "prometheus", 7, opUpgrade)
	ns, name, rev, op, err := parseRequestID(id)
	if err != nil {
		t.Fatalf("parseRequestID(%q): %v", id, err)
	}
	if ns != "monitoring" || name != "prometheus" || rev != 7 || op != opUpgrade {
		t.Fatalf("got %s/%s@%d:%s, want monitoring/prometheus@7:upgrade", ns, name, rev, op)
	}
}

func TestParseRequestID_RejectsMalformed(t *testing.T) {
	// A request id Status cannot parse must error rather than silently default
	// to install or to an empty namespace, which would send the lookup at the
	// wrong release.
	for _, id := range []string{
		"",
		"ns/name",
		"ns/name@x:install",
		"ns/name:install",
		"name@1:install",  // no namespace
		"/name@1:install", // empty namespace
		"ns/@1:install",   // empty name
		"a/b/c@1:install", // too many segments
	} {
		if _, _, _, _, err := parseRequestID(id); err == nil {
			t.Errorf("parseRequestID(%q) accepted a malformed id", id)
		}
	}
}

func TestNativeIDUnless(t *testing.T) {
	if got := nativeIDUnless(true, "ns", "name"); got != "" {
		t.Errorf("withheld native id = %q, want empty", got)
	}
	if got := nativeIDUnless(false, "ns", "name"); got != "ns/name" {
		t.Errorf("native id = %q, want ns/name", got)
	}
}

func TestStalled_OnlyAfterTwiceTheOperationTimeout(t *testing.T) {
	pendingSince := func(d time.Duration) *release.Release {
		return &release.Release{Info: &release.Info{
			Status:       release.StatusPendingInstall,
			LastDeployed: helmtime.Time{Time: time.Now().Add(-d)},
		}}
	}

	// A slow pre-install hook is indistinguishable from a dead process, so the
	// window has to be wide enough that healing never races a live install.
	if stalled(pendingSince(time.Minute)) {
		t.Error("a one-minute-old pending release was called stalled")
	}
	if !stalled(pendingSince(3 * defaultTimeoutSeconds * time.Second)) {
		t.Error("a long-abandoned pending release was not called stalled")
	}
	if stalled(&release.Release{Info: &release.Info{Status: release.StatusPendingInstall}}) {
		t.Error("a release with no timestamp was called stalled")
	}
}
