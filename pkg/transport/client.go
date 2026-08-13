// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"sync"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// Client wraps the Kubernetes clientset with plugin configuration.
type Client struct {
	*kubernetes.Clientset
	Dynamic dynamic.Interface
	Config  *config.Config

	mapperMu sync.Mutex
	mapper   meta.RESTMapper

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
func (c *Client) ResolveKind(kind string) (schema.GroupVersionResource, bool, bool) {
	c.kindMu.Lock()
	defer c.kindMu.Unlock()

	if c.kindIndex == nil {
		lists, err := c.Discovery().ServerPreferredResources()
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
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	if c.versionSet {
		return c.version, nil
	}
	v, err := config.ResolveK8sVersion(ctx, c.Config, c.Discovery())
	if err != nil {
		return "", err
	}
	c.version = v
	c.versionSet = true
	return v, nil
}

// ResolveMapping maps an apiVersion+kind to its GVR and namespaced scope using
// a discovery-backed RESTMapper. The mapper is built lazily and cached. If the
// kind is not found (e.g. a CRD installed after the mapper was first built),
// the mapper is reset once and the lookup retried, so an operator can install
// a CRD and apply an instance of it in the same plugin process.
func (c *Client) ResolveMapping(apiVersion, kind string) (schema.GroupVersionResource, bool, error) {
	c.mapperMu.Lock()
	if c.mapper == nil {
		c.mapper = restmapper.NewDeferredDiscoveryRESTMapper(
			memory.NewMemCacheClient(c.Discovery()),
		)
	}
	mapper := c.mapper
	c.mapperMu.Unlock()

	gvr, namespaced, err := resolveMappingWith(mapper, apiVersion, kind)
	if err == nil {
		return gvr, namespaced, nil
	}

	// Reset-on-miss: the CRD may have been installed mid-session.
	if r, ok := mapper.(meta.ResettableRESTMapper); ok {
		r.Reset()
		return resolveMappingWith(mapper, apiVersion, kind)
	}
	return schema.GroupVersionResource{}, false, err
}

// ResetMapper discards the cached discovery/RESTMapper so the next
// ResolveMapping re-fetches from the apiserver. Use after an apply fails because
// a kind is not (yet) served — e.g. its CRD was just created or recreated — so a
// stale "kind exists" cache entry doesn't keep resolving to a dead endpoint.
func (c *Client) ResetMapper() {
	c.mapperMu.Lock()
	if r, ok := c.mapper.(meta.ResettableRESTMapper); ok {
		r.Reset()
	} else {
		c.mapper = nil
	}
	c.mapperMu.Unlock()
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
func NewClient(cfg *config.Config) (*Client, error) {
	restConfig, err := cfg.ToK8sConfig()
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
		Clientset: clientset,
		Dynamic:   dynamicClient,
		Config:    cfg,
	}, nil
}
