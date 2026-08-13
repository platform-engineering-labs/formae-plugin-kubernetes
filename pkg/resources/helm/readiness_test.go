//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"strings"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
)

// deployedRelease is what Helm records under Wait=false as soon as the apiserver
// accepts the manifests — long before anything is actually running, and it never
// changes again no matter what the objects do afterwards.
func deployedRelease(deployedAgo time.Duration, timeoutLabel string) *release.Release {
	labels := map[string]string{formaeManagedLabel: "true"}
	if timeoutLabel != "" {
		labels[formaeTimeoutLabel] = timeoutLabel
	}
	return &release.Release{
		Name:      "podinfo",
		Namespace: "apps",
		Version:   1,
		Labels:    labels,
		Info: &release.Info{
			Status:       release.StatusDeployed,
			LastDeployed: helmtime.Time{Time: time.Now().Add(-deployedAgo)},
		},
	}
}

// A workload that is merely slow to come up must keep polling: image pulls and
// pre-start work legitimately take minutes.
func TestReadinessProgress_WithinWindow_StaysInProgress(t *testing.T) {
	rel := deployedRelease(30*time.Second, "600")

	got := readinessProgress(rel, nil, "", "apps/podinfo@1:install", "waiting for Deployment/podinfo")

	if got.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("OperationStatus = %s, want InProgress", got.ProgressResult.OperationStatus)
	}
}

// The stuck-command case. A typo'd image tag leaves the release `deployed` with
// a Pod in ImagePullBackOff forever: the record never changes, so without a
// deadline Status answers InProgress for eternity and the command never reaches
// a verdict. The host has no cap of its own — it only fails an operation when
// the plugin goes silent, not when it keeps reporting progress.
func TestReadinessProgress_PastWindow_Fails(t *testing.T) {
	rel := deployedRelease(25*time.Minute, "600") // 2 x 600s exceeded

	got := readinessProgress(rel, nil, "", "apps/podinfo@1:install", "waiting for Deployment/podinfo")

	if got.ProgressResult.OperationStatus != resource.OperationStatusFailure {
		t.Fatalf("OperationStatus = %s, want Failure", got.ProgressResult.OperationStatus)
	}
	// The reason a human needs: which object never came up.
	if !strings.Contains(got.ProgressResult.StatusMessage, "Deployment/podinfo") {
		t.Errorf("StatusMessage = %q, want it to name the object that never became ready",
			got.ProgressResult.StatusMessage)
	}
}

// An operation this process is still running is never declared dead, however
// long its hooks take — the same asymmetry stalled() already relies on.
func TestReadinessProgress_OperationStillRunning_StaysInProgress(t *testing.T) {
	rel := deployedRelease(25*time.Minute, "600")
	flight := &inflight{op: opInstall, revision: 1, deadline: time.Now().Add(5 * time.Minute)}

	got := readinessProgress(rel, flight, "", "apps/podinfo@1:install", "waiting for Deployment/podinfo")

	if got.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("OperationStatus = %s, want InProgress while this process drives it",
			got.ProgressResult.OperationStatus)
	}
}
