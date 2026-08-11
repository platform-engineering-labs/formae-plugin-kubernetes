//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
)

func storedRelease(c *chart.Chart) *release.Release {
	return &release.Release{
		Name:      "kratos",
		Namespace: "apps",
		Version:   1,
		Labels:    map[string]string{formaeManagedLabel: "true"},
		Chart:     c,
	}
}

func wantChart(name, version string) *releaseProperties {
	return &releaseProperties{Chart: name, Version: version}
}

// A chart with no subcharts round-trips through the release record intact, so
// re-applying the deployed version needs no repository access.
func TestStoredChartUsable_SelfContainedChart(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{Name: "podinfo", Version: "6.5.0"}}

	if !storedChartUsable(storedRelease(c), wantChart("podinfo", "6.5.0")) {
		t.Fatal("a self-contained chart at the deployed version should be reused")
	}
}

// The one Helm does not round-trip. chart.Chart.dependencies is unexported and
// carries no JSON tag (helm/pkg/chart/chart.go:56), so the subcharts are gone
// from the chart Helm reads back out of the release Secret — while
// Metadata.Dependencies, which is serialized, still lists them.
//
// Rendering that remnant fails outright for any chart whose templates call a
// helper defined in a library dependency (every ory chart, for one):
//
//	template: no template "ory.extraEnvContainsEnvName" associated with template "gotpl"
func TestStoredChartComplete_RejectsChartWhoseSubchartsWereNotStored(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{
		Name:    "kratos",
		Version: "0.63.0",
		Dependencies: []*chart.Dependency{
			{Name: "ory-commons", Version: "0.1.0"},
		},
	}}
	// Deliberately not SetDependencies: this is the shape that comes back out of
	// the release record.

	if storedChartComplete(storedRelease(c)) {
		t.Fatal("a stored chart whose subcharts were dropped must not be rendered: " +
			"it fails on any helper the dependency defines")
	}
}

// A chart that really does have its dependencies attached is fine to render.
func TestStoredChartComplete_SubchartsPresent(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{
		Name:         "kratos",
		Version:      "0.63.0",
		Dependencies: []*chart.Dependency{{Name: "ory-commons", Version: "0.1.0"}},
	}}
	c.SetDependencies(&chart.Chart{Metadata: &chart.Metadata{Name: "ory-commons", Version: "0.1.0"}})

	if !storedChartComplete(storedRelease(c)) {
		t.Fatal("a chart with its dependencies attached should be rendered from")
	}
}

// A chart with no subcharts at all is complete by construction.
func TestStoredChartComplete_NoSubcharts(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{Name: "podinfo", Version: "6.5.0"}}

	if !storedChartComplete(storedRelease(c)) {
		t.Fatal("a chart that declares no dependencies is complete")
	}
}

// settled() renders nothing, so an incomplete stored chart must not stop it
// concluding a no-op — otherwise a re-driven Create on a subchart chart plans an
// upgrade and re-runs the chart's hooks.
func TestStoredChartUsable_IgnoresDroppedSubcharts(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{
		Name:         "kratos",
		Version:      "0.63.0",
		Dependencies: []*chart.Dependency{{Name: "ory-commons", Version: "0.1.0"}},
	}}

	if !storedChartUsable(storedRelease(c), wantChart("kratos", "0.63.0")) {
		t.Fatal("version and name still match; the dropped subcharts are a rendering " +
			"concern, not a settled-state one")
	}
}
