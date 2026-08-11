//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
)

// uninstallingRelease is what the storage holds while Helm is tearing a release
// down: the record is moved to `uninstalling` before the first object is
// deleted, and purged only after WaitForDelete and the post-delete hooks.
func uninstallingRelease(timeoutLabel string) *release.Release {
	labels := map[string]string{formaeManagedLabel: "true"}
	if timeoutLabel != "" {
		labels[formaeTimeoutLabel] = timeoutLabel
	}
	return &release.Release{
		Name:      "kratos",
		Namespace: "apps",
		Version:   3,
		Labels:    labels,
		Info: &release.Info{
			Status:       release.StatusUninstalling,
			LastDeployed: helmtime.Time{Time: time.Now().Add(-30 * time.Second)},
		},
	}
}

// The case that matters for every chart with a pre-delete hook or a Pod with a
// non-trivial terminationGracePeriodSeconds: formae's own uninstall is still
// running when the first Status poll lands, ~20s later by default.
//
// Without a registered flight this reports Failure/ResourceConflict, telling the
// agent a live uninstall was abandoned and to re-drive Delete — which starts a
// second concurrent uninstall of the same release.
func TestDeleteStatus_OwnUninstallStillRunning_ReportsInProgress(t *testing.T) {
	rel := uninstallingRelease("")
	flight := deleteFlight(rel)

	got := deleteStatus(rel, &flight, "apps", "kratos", "apps/kratos@3:delete")

	if got.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("OperationStatus = %s (%s), want InProgress",
			got.OperationStatus, got.StatusMessage)
	}
}

// A record still present with nothing driving it is the abandoned case, and it
// must stay a recoverable failure: uninstall is idempotent, so re-driving it is
// the right move.
func TestDeleteStatus_NoOperationBehindIt_ReportsAbandoned(t *testing.T) {
	got := deleteStatus(uninstallingRelease(""), nil, "apps", "kratos", "apps/kratos@3:delete")

	if got.OperationStatus != resource.OperationStatusFailure {
		t.Fatalf("OperationStatus = %s, want Failure", got.OperationStatus)
	}
	if got.ErrorCode != resource.OperationErrorCodeResourceConflict {
		t.Errorf("ErrorCode = %s, want ResourceConflict so the agent re-drives Delete", got.ErrorCode)
	}
}

// The uninstall must be bounded by the timeout the release was installed with,
// not by the package default. Otherwise a release with timeoutSeconds = 1800 has
// its uninstall killed at 600s while stalled() waits 2x1800s before saying so —
// 50 minutes of InProgress on work nothing is doing.
func TestDeleteFlight_DeadlineFromRecordedTimeout(t *testing.T) {
	f := deleteFlight(uninstallingRelease("1800"))

	if f.op != opDelete {
		t.Errorf("op = %q, want %q", f.op, opDelete)
	}
	want := time.Now().Add(1800 * time.Second)
	if f.deadline.Before(want.Add(-time.Minute)) || f.deadline.After(want.Add(time.Minute)) {
		t.Errorf("deadline = %s, want ~%s (the release's recorded timeout)", f.deadline, want)
	}
}

func TestDeleteFlight_FallsBackToDefaultTimeout(t *testing.T) {
	f := deleteFlight(uninstallingRelease(""))

	want := time.Now().Add(defaultTimeoutSeconds * time.Second)
	if f.deadline.Before(want.Add(-time.Minute)) || f.deadline.After(want.Add(time.Minute)) {
		t.Errorf("deadline = %s, want ~%s (the package default)", f.deadline, want)
	}
}
