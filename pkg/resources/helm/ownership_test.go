//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/release"
)

// The ownership marker is this plugin's own bookkeeping and must not come back
// out of Read as though the user had declared it.
//
// withoutSystemLabels already makes this argument for Helm's reserved names
// (release.go:186-192): a label the plugin did not get from desired state, left
// in the reported properties, "leaks into Read, gets copied into an extracted
// forma", and shows up as a diff against a forma that never mentioned it.
// formae.dev/managed is in exactly that category — releaseLabels stamps it on
// every install and upgrade unconditionally (release.go:171-180) — but
// withoutSystemLabels only drops driver.GetSystemLabels(), so the marker
// survives into propertiesFromRelease's Metadata.Labels (release.go:629).
//
// The schema marks labels hasProviderDefault (Release.pkl:223), which is the
// intended absorber. That only holds if the host diffs the map key-by-key; if it
// compares whole maps, every formae-managed release drifts forever on a label
// formae injected itself. Not reporting the marker at all removes the
// dependency on which semantics the host happens to implement.
//
// Falsifiable by: deleting the marker filter from propertiesFromRelease.
func TestPropertiesFromRelease_OmitsTheOwnershipMarker(t *testing.T) {
	rel := relAt(release.StatusDeployed, 1, time.Minute) // relAt stamps the marker
	rel.Labels["team"] = "platform"                      // and a label the user did declare

	got := propertiesFromRelease(rel).Metadata.Labels

	if _, leaked := got[formaeManagedLabel]; leaked {
		t.Errorf("Read reported %s=%q; the marker is our bookkeeping, not desired state",
			formaeManagedLabel, got[formaeManagedLabel])
	}
	if got["team"] != "platform" {
		t.Errorf("labels = %v; a label the user declared must still round-trip", got)
	}
}

// A release whose only label is the marker must report no labels at all, not an
// empty map — the same nil-not-empty contract withoutSystemLabels already keeps
// (release.go:198-206), so an extracted forma omits the field instead of
// declaring `labels {}`.
func TestPropertiesFromRelease_MarkerOnlyReportsNoLabels(t *testing.T) {
	rel := relAt(release.StatusDeployed, 1, time.Minute)

	if got := propertiesFromRelease(rel).Metadata.Labels; got != nil {
		t.Errorf("labels = %v (len %d), want nil", got, len(got))
	}
}
