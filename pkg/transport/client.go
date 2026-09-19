// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// Client wraps the Kubernetes clientset with plugin configuration.
type Client struct {
	*kubernetes.Clientset
	Dynamic dynamic.Interface
	Config  *config.Config

	mapperMu   sync.Mutex
	groups     []*restmapper.APIGroupResources
	restConfig *rest.Config
	oidcTokens auth.TokenSource

	versionMu  sync.Mutex
	version    string
	versionSet bool

	kindMu    sync.Mutex
	kindIndex map[string]kindMapping
}

// kindMapping is a resolved kind, without its group having to be known up front.
type kindMapping struct {
	GVR        schema.GroupVersionResource
	Namespaced bool
}

// ResolveKind maps a bare kind name to its GVR, without an apiVersion.
//
// ResolveMapping needs a group, which is fine when the caller knows the type it
// is working with. Walking ownerReferences does not: an ownerReference on the
// object discovery found gives a kind, and the kind of the *starting* object is
// all a resource type like K8S::Core::Secret carries. Guessing the group from the
// resource type would need a table that rots whenever an API graduates a version.
//
// Built once from the server's preferred resources, which the discovery client
// already caches, and held for the life of the Client. First match wins: a kind
// name served by two groups is ambiguous and picking the preferred version is the
// same choice kubectl makes.
func (c *Client) ResolveKind(ctx context.Context, kind string) (schema.GroupVersionResource, bool, bool) {
	if err := c.checkOperation(ctx); err != nil {
		return schema.GroupVersionResource{}, false, false
	}
	c.kindMu.Lock()
	defer c.kindMu.Unlock()

	if c.kindIndex == nil {
		disc, err := c.operationDiscovery(ctx)
		if err != nil {
			return schema.GroupVersionResource{}, false, false
		}
		lists, err := disc.ServerPreferredResources()
		// A partial result is normal and usable: an unavailable aggregated API
		// server makes this return both an error and the groups that did answer.
		if lists == nil && err != nil {
			return schema.GroupVersionResource{}, false, false
		}
		c.kindIndex = map[string]kindMapping{}
		for _, list := range lists {
			gv, parseErr := schema.ParseGroupVersion(list.GroupVersion)
			if parseErr != nil {
				continue
			}
			for _, r := range list.APIResources {
				if _, exists := c.kindIndex[r.Kind]; exists {
					continue
				}
				c.kindIndex[r.Kind] = kindMapping{
					GVR:        gv.WithResource(r.Name),
					Namespaced: r.Namespaced,
				}
			}
		}
	}

	m, ok := c.kindIndex[kind]
	return m.GVR, m.Namespaced, ok
}

// ResolveVersion returns the target cluster's normalized MAJOR.MINOR K8s
// version, resolved once and cached. Resolution follows config.ResolveK8sVersion
// (target-config override → FORMAE_K8S_VERSION env → live
// Discovery().ServerVersion()).
//
// Only a successful result is cached. A failure (e.g. a transient TLS/handshake
// timeout to the apiserver) is returned but NOT memoized, so the next operation
// retries. Caching an error here would be sticky for the life of the
// process-cached Client and would silently disable version gating until the
// agent restarts.
func (c *Client) ResolveVersion(ctx context.Context) (string, error) {
	if err := c.checkOperation(ctx); err != nil {
		return "", err
	}
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	if c.versionSet {
		return c.version, nil
	}
	disc, err := c.operationDiscovery(ctx)
	if err != nil {
		return "", err
	}
	v, err := config.ResolveK8sVersion(ctx, c.Config, disc)
	if err != nil {
		return "", err
	}
	c.version = v
	c.versionSet = true
	return v, nil
}

// ResolveMapping maps an apiVersion+kind to its GVR and namespaced scope using
// a discovery-backed RESTMapper. Only discovered API data is cached. If the
// kind is not found (e.g. a CRD installed after the mapper was first built),
// the mapper is reset once and the lookup retried, so an operator can install
// a CRD and apply an instance of it in the same plugin process.
func (c *Client) ResolveMapping(ctx context.Context, apiVersion, kind string) (schema.GroupVersionResource, bool, error) {
	if err := c.checkOperation(ctx); err != nil {
		return schema.GroupVersionResource{}, false, err
	}
	c.mapperMu.Lock()
	defer c.mapperMu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if c.groups == nil {
			disc, err := c.operationDiscovery(ctx)
			if err != nil {
				return schema.GroupVersionResource{}, false, err
			}
			groups, err := restmapper.GetAPIGroupResources(disc)
			if err != nil {
				return schema.GroupVersionResource{}, false, err
			}
			c.groups = groups
		}
		// Only API data persists. The mapper and any discovery adapter belong to
		// this call; a CRD miss invalidates data and rediscovers once.
		gvr, namespaced, err := resolveMappingWith(restmapper.NewDiscoveryRESTMapper(c.groups), apiVersion, kind)
		if err == nil {
			return gvr, namespaced, nil
		}
		c.groups = nil
		if attempt == 1 {
			return schema.GroupVersionResource{}, false, err
		}
	}
	panic("unreachable")
}

// ResetMapper discards discovery data after a resource or CRD changes.
func (c *Client) ResetMapper() {
	c.mapperMu.Lock()
	c.groups = nil
	c.mapperMu.Unlock()
	c.kindMu.Lock()
	c.kindIndex = nil
	c.kindMu.Unlock()
}

// operationDiscovery installs the callback adapter outside the reusable bearer
// transport. Never assign this client or its rest.Config back to Client.
func (c *Client) operationDiscovery(ctx context.Context) (discovery.DiscoveryInterface, error) {
	if c.restConfig == nil {
		if c.Clientset == nil {
			return nil, nil
		}
		return c.Discovery(), nil
	}
	cfg := rest.CopyConfig(c.restConfig)
	cfg.Wrap(func(base http.RoundTripper) http.RoundTripper { return auth.WithOperationContext(base, ctx) })
	return discovery.NewDiscoveryClientForConfig(cfg)
}

// resolveMappingWith performs a single GVK->GVR lookup against the given mapper.
// Split out so it can be unit-tested with a static mapper.
func resolveMappingWith(mapper meta.RESTMapper, apiVersion, kind string) (schema.GroupVersionResource, bool, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("parse apiVersion %q: %w", apiVersion, err)
	}
	mapping, err := mapper.RESTMapping(gv.WithKind(kind).GroupKind(), gv.Version)
	if err != nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("no REST mapping for %s/%s: %w", apiVersion, kind, err)
	}
	return mapping.Resource, mapping.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// NewClient creates a new Kubernetes client from the provided config.
func NewClient(ctx context.Context, cfg *config.Config) (*Client, error) {
	var restConfig *rest.Config
	var tokens auth.TokenSource
	var err error
	if cfg.UsesOidc() {
		restConfig, tokens, err = cfg.OidcClientConfig()
	} else {
		restConfig, err = cfg.ToK8sConfig(ctx)
	}
	if err != nil {
		return nil, err
	}

	// Suppress K8S API deprecation warnings (e.g., Endpoints deprecated in v1.33+)
	// that would otherwise be logged to stderr and treated as plugin errors by Formae.
	restConfig.WarningHandler = rest.NoWarnings{}

	// Disable client-go's local QPS/Burst throttle. Formae core already gates
	// plugin invocations via Plugin.RateLimit(); a second token bucket inside
	// client-go just adds redundant client-side delays and emits klog INFO
	// lines ("Waited before sending request") that Formae mis-classifies as
	// plugin errors. Apiserver-side APF (429 + Retry-After) remains the
	// authoritative back-pressure mechanism.
	restConfig.QPS = -1
	restConfig.Burst = -1

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	return &Client{
		Clientset:  clientset,
		Dynamic:    dynamicClient,
		Config:     cfg,
		restConfig: restConfig,
		oidcTokens: tokens,
	}, nil
}

func (c *Client) checkOperation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Config != nil && c.Config.UsesOidc() {
		return c.Config.CheckRuntime(ctx)
	}
	return nil
}

// CallbackRESTConfig returns a copy for a handler-owned Helm adapter. The
// underlying auth cache is shared, but the adapter/context must never be stored.
func (c *Client) CallbackRESTConfig(ctx context.Context) (*rest.Config, error) {
	if err := c.checkOperation(ctx); err != nil {
		return nil, err
	}
	return rest.CopyConfig(c.restConfig), nil
}

// OidcWorkerConfig returns a queue-only worker config and the separately held
// callback source. Only the former may be handed to a detached worker.
func (c *Client) OidcWorkerConfig(ctx context.Context, worker auth.TokenSource) (*rest.Config, auth.TokenSource, error) {
	rc, _, err := c.Config.OidcWorkerConfig(ctx, worker)
	if err != nil {
		return nil, nil, err
	}
	return rc, c.oidcTokens, nil
}
