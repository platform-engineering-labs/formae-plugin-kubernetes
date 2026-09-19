// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/registry"
	"k8s.io/client-go/rest"
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
func newActionConfig(ctx context.Context, cfg *config.Config, namespace string) (*action.Configuration, error) {
	return buildActionConfig(ctx, cfg, namespace, true)
}

// Legacy workers outlive the submitting callback. Resolve legacy metadata with
// its live context, but retain no operation adapter in the worker config.
func newAsyncActionConfig(ctx context.Context, cfg *config.Config, namespace string) (*action.Configuration, error) {
	if cfg != nil && cfg.UsesOidc() {
		return nil, fmt.Errorf("OIDC Helm workers require the credential bridge")
	}
	return buildActionConfig(ctx, cfg, namespace, false)
}

func buildActionConfig(ctx context.Context, cfg *config.Config, namespace string, bind bool) (*action.Configuration, error) {
	restCfg, err := cfg.ToK8sConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("build rest config: %w", err)
	}

	if bind {
		restCfg.Wrap(func(base http.RoundTripper) http.RoundTripper { return auth.WithOperationContext(base, ctx) })
	}
	return actionConfigFromREST(restCfg, namespace)
}

func actionConfigFromREST(restCfg *rest.Config, namespace string) (*action.Configuration, error) {
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

// A worker adapter binds only a fresh Background-derived action context. Helm
// Uninstall and lazy discovery otherwise discard caller contexts internally.
func newBridgeActionConfig(ctx context.Context, restCfg *rest.Config, namespace string, b *authBridge) (*action.Configuration, error) {
	restCfg = rest.CopyConfig(restCfg)
	restCfg.Timeout = min(b.window+30*time.Second, time.Until(b.deadline))
	restCfg.Dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	restCfg.Wrap(func(base http.RoundTripper) http.RoundTripper { return auth.WithOperationContext(base, ctx) })
	return actionConfigFromREST(restCfg, namespace)
}

func (r *Release) callbackActionConfig(ctx context.Context, namespace string) (*action.Configuration, error) {
	if err := r.Config.CheckRuntime(ctx); err != nil {
		return nil, err
	}
	restCfg, err := r.Client.CallbackRESTConfig(ctx)
	if err != nil {
		return nil, err
	}
	restCfg.Wrap(func(base http.RoundTripper) http.RoundTripper { return auth.WithOperationContext(base, ctx) })
	return actionConfigFromREST(restCfg, namespace)
}
