// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"log"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/helm"
	"github.com/platform-engineering-labs/formae/pkg/plugin/sdk"
)

// drainTimeout bounds how long a graceful stop waits for in-flight Helm
// operations to unwind. Only a single release-record write has to land, so this
// is generous; it is a ceiling on shutdown, not a target.
const drainTimeout = 10 * time.Second

func main() {
	sdk.RunWithManifest(&Plugin{}, sdk.RunConfig{BeforeStop: drainHelmBeforeStop})
}

// drainHelmBeforeStop cancels in-flight Helm operations after the SDK receives
// SIGINT or SIGTERM and before it stops the plugin node. This ordering gives
// Helm a bounded chance to leave a release recoverable by the next apply.
//
// Cancelling makes Helm run failRelease (install.go:411, upgrade.go:399), which
// records the release as `failed` — a state the next apply simply upgrades over.
// Being killed without cancelling leaves it `pending-install`, which Helm
// refuses to install OR upgrade and which only `helm uninstall` clears. The
// difference between those two outcomes is one Secret write.
//
// The callback remains best-effort: it waits at most ten seconds. Parent death
// and agent shutdown still deliver SIGKILL, which cannot run a callback, so
// stalled-release detection and reapply recovery remain the floor for those
// paths.
func drainHelmBeforeStop() {
	if helm.DrainInFlight(drainTimeout) {
		return
	}
	// Worth saying out loud: whatever did not unwind is a release left pending
	// in the cluster, and the operator will meet it as a blocked apply later.
	log.Printf("helm: in-flight operations did not unwind within %s; "+
		"releases left pending may need `helm uninstall` before the next apply", drainTimeout)
}
