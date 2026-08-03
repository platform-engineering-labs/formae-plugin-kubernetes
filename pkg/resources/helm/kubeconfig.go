// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// restGetter adapts the plugin's already-authenticated *rest.Config to the
// genericclioptions.RESTClientGetter that action.Configuration.Init wants.
//
// Deliberately NOT built from clientcmd's default loading rules: the plugin's
// auth may be cloud-provider based (EKS/GKE/AKS token exchange), and falling
// back to reading ~/.kube/config would silently target whatever cluster the
// host machine happens to point at. Every method here derives from the
// rest.Config the plugin already resolved for this target.
type restGetter struct {
	cfg *rest.Config

	// namespace is only consumed via ToRawKubeConfigLoader, which Helm uses to
	// resolve the default namespace for manifest objects that omit one.
	namespace string

	mu     sync.Mutex
	disc   discovery.CachedDiscoveryInterface
	mapper meta.RESTMapper
}

func newRESTGetter(cfg *rest.Config, namespace string) *restGetter {
	return &restGetter{cfg: cfg, namespace: namespace}
}

func (g *restGetter) ToRESTConfig() (*rest.Config, error) {
	return g.cfg, nil
}

// ToDiscoveryClient returns a memory-cached discovery client. Helm calls this
// repeatedly within a single action (capabilities, then every manifest object's
// GVK lookup); without the cache that is one apiserver round-trip per call.
func (g *restGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.disc != nil {
		return g.disc, nil
	}
	dc, err := discovery.NewDiscoveryClientForConfig(g.cfg)
	if err != nil {
		return nil, err
	}
	g.disc = memory.NewMemCacheClient(dc)
	return g.disc, nil
}

func (g *restGetter) ToRESTMapper() (meta.RESTMapper, error) {
	g.mu.Lock()
	if g.mapper != nil {
		defer g.mu.Unlock()
		return g.mapper, nil
	}
	g.mu.Unlock()

	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	// Deferred: a chart's CRDs are installed in the same action that later
	// applies CRs of that kind, so the mapper must be able to re-discover.
	g.mapper = restmapper.NewDeferredDiscoveryRESTMapper(dc)
	return g.mapper, nil
}

// ToRawKubeConfigLoader returns an in-memory config carrying only the default
// namespace. An empty clientcmdapi.Config is intentional — returning the real
// on-disk kubeconfig here is what would let host state leak into a targeted
// operation.
func (g *restGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewDefaultClientConfig(
		*clientcmdapi.NewConfig(),
		&clientcmd.ConfigOverrides{
			Context: clientcmdapi.Context{Namespace: g.namespace},
		},
	)
}
