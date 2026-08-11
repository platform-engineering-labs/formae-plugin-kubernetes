//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"errors"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
)

func selfContained() *chart.Chart {
	return &chart.Chart{Metadata: &chart.Metadata{Name: "podinfo", Version: "6.5.0"}}
}

// subchartsDropped is the shape a chart comes back in from the release record:
// Metadata.Dependencies lists subcharts that the unexported dependencies field
// no longer holds.
func subchartsDropped() *chart.Chart {
	return &chart.Chart{Metadata: &chart.Metadata{
		Name:         "openfga",
		Version:      "0.2.31",
		Dependencies: []*chart.Dependency{{Name: "common", Version: "2.x"}},
	}}
}

func fetchFails() func() (*chart.Chart, error) {
	return func() (*chart.Chart, error) {
		return nil, errors.New(`chart "openfga" needs a repoURL`)
	}
}

// Re-applying the deployed version of a self-contained chart needs no repository
// access at all.
func TestChooseChart_ReusesCompleteStoredChart(t *testing.T) {
	fetched := false
	got, err := chooseChart(storedRelease(selfContained()), wantChart("podinfo", "6.5.0"),
		func() (*chart.Chart, error) { fetched = true; return selfContained(), nil })
	if err != nil {
		t.Fatalf("chooseChart: %v", err)
	}
	if fetched {
		t.Error("fetched a chart that was already stored complete")
	}
	if got.Metadata.Name != "podinfo" {
		t.Errorf("chart = %q, want podinfo", got.Metadata.Name)
	}
}

// When the subcharts were dropped and there is somewhere to fetch from, fetch:
// rendering the remnant fails on any helper a dependency defines.
func TestChooseChart_RefetchesWhenSubchartsWereDropped(t *testing.T) {
	fresh := subchartsDropped()
	fresh.SetDependencies(&chart.Chart{Metadata: &chart.Metadata{Name: "common", Version: "2.x"}})

	got, err := chooseChart(storedRelease(subchartsDropped()), wantChart("openfga", "0.2.31"),
		func() (*chart.Chart, error) { return fresh, nil })
	if err != nil {
		t.Fatalf("chooseChart: %v", err)
	}
	if len(got.Dependencies()) == 0 {
		t.Error("returned the stored remnant rather than the fetched chart")
	}
}

// The case that matters for adoption. Helm never records which repository a
// release came from, so a release adopted through `formae extract` has no
// repoURL and there is nothing to fetch from. Refusing the stored chart here
// fails the upgrade outright, which is worse than rendering a chart that may
// still be renderable — and if a dropped subchart does matter, Helm says which
// template it could not find.
func TestChooseChart_FallsBackToStoredChartWhenThereIsNothingToFetchFrom(t *testing.T) {
	got, err := chooseChart(storedRelease(subchartsDropped()), wantChart("openfga", "0.2.31"),
		fetchFails())
	if err != nil {
		t.Fatalf("chooseChart returned an error where the stored chart was usable: %v", err)
	}
	if got == nil || got.Metadata.Name != "openfga" {
		t.Fatal("expected the stored chart as a fallback")
	}
}

// With no usable stored chart the fetch error is the answer — it names what the
// user has to supply.
func TestChooseChart_SurfacesFetchErrorWhenNothingIsStored(t *testing.T) {
	_, err := chooseChart(storedRelease(selfContained()), wantChart("openfga", "9.9.9"), fetchFails())
	if err == nil {
		t.Fatal("expected the fetch error to surface")
	}
}
