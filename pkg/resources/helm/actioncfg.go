// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/registry"
)

// helmDriver selects Helm's release storage backend. "secret" is Helm's own
// default and the only one `helm list` looks at without extra flags, so the
// releases formae creates are the same objects the Helm CLI sees.
const helmDriver = "secret"

// newActionConfig builds a Helm action.Configuration bound to one namespace.
//
// Cheap to construct — the underlying clients are lazy — so it is built per
// operation rather than cached. Caching it would pin a namespace and a
// discovery snapshot for the life of the process, and Helm mutates fields on
// the config during an action.
func newActionConfig(cfg *config.Config, namespace string) (*action.Configuration, error) {
	restCfg, err := cfg.ToK8sConfig()
	if err != nil {
		return nil, fmt.Errorf("build rest config: %w", err)
	}

	conf := new(action.Configuration)
	if err := conf.Init(newRESTGetter(restCfg, namespace), namespace, helmDriver, debugLog); err != nil {
		return nil, fmt.Errorf("init helm action config: %w", err)
	}

	// Required for oci:// chart references. Without it Helm errors on any OCI
	// pull, which is how most charts are distributed now.
	rc, err := registry.NewClient(registry.ClientOptEnableCache(true))
	if err != nil {
		return nil, fmt.Errorf("init helm registry client: %w", err)
	}
	conf.RegistryClient = rc

	return conf, nil
}

// debugLog swallows Helm's debug output. Helm writes progress chatter here, and
// formae treats plugin stderr as an error signal — the same reason
// transport.NewClient sets rest.NoWarnings and disables client-go's throttle
// logging.
func debugLog(string, ...interface{}) {}
