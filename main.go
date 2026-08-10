// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/helm"
	"github.com/platform-engineering-labs/formae/pkg/plugin/sdk"
)

// drainTimeout bounds how long a graceful stop waits for in-flight Helm
// operations to unwind. Only a single release-record write has to land, so this
// is generous; it is a ceiling on shutdown, not a target.
const drainTimeout = 10 * time.Second

func main() {
	go drainHelmOnSignal()
	sdk.RunWithManifest(&Plugin{}, sdk.RunConfig{})
}

// drainHelmOnSignal cancels in-flight Helm operations when the plugin is asked
// to stop, so a graceful restart leaves recoverable releases behind.
//
// Cancelling makes Helm run failRelease (install.go:411, upgrade.go:399), which
// records the release as `failed` — a state the next apply simply upgrades over.
// Being killed without cancelling leaves it `pending-install`, which Helm
// refuses to install OR upgrade and which only `helm uninstall` clears. The
// difference between those two outcomes is one Secret write.
//
// Best-effort by construction. The SDK installs its own SIGTERM handler and
// calls node.Stop() (pkg/plugin/run.go:190-197); Go delivers to every
// signal.Notify receiver concurrently and offers no ordering hook, so this
// races the node teardown that follows. Winning is the common case because the
// write is a single API call, and losing costs nothing — the outcome is then
// exactly what it was before this existed. SIGKILL is out of reach either way,
// and stalled() plus the recovery message in stalledMessage remain the floor.
func drainHelmOnSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	if helm.DrainInFlight(drainTimeout) {
		return
	}
	// Worth saying out loud: whatever did not unwind is a release left pending
	// in the cluster, and the operator will meet it as a blocked apply later.
	log.Printf("helm: in-flight operations did not unwind within %s; "+
		"releases left pending may need `helm uninstall` before the next apply", drainTimeout)
}
