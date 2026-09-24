// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

const cacheTTL = 10 * time.Minute

type entry struct {
	client    *Client
	createdAt time.Time
}
type clientBuilder func(context.Context, *config.Config) (*Client, error)
type metadataAccessor func(context.Context) (plugin.OidcOperationInfo, bool)

// ClientCache belongs to one plugin instance. Entries retain immutable auth
// configuration and discovery data, never a callback context or its adapter.
type ClientCache struct {
	mu       sync.RWMutex
	entries  map[string]entry
	build    clientBuilder
	metadata metadataAccessor
	ttl      time.Duration
}

func NewClientCache() *ClientCache { return newClientCache(NewClient, plugin.OidcOperationMetadata) }
func newClientCache(build clientBuilder, metadata metadataAccessor) *ClientCache {
	return &ClientCache{entries: make(map[string]entry), build: build, metadata: metadata, ttl: cacheTTL}
}

// Get isolates every effective identity and CA. Only broker-consuming targets
// include the trusted binding. Old agents without metadata get call-local clients.
func (c *ClientCache) Get(ctx context.Context, cfg *config.Config) (*Client, error) {
	if err := cfg.CheckRuntime(ctx); err != nil {
		return nil, err
	}
	key, err := cfg.AuthFingerprint()
	if err != nil {
		return nil, fmt.Errorf("client cache: %w", err)
	}
	if cfg.UsesOidc() {
		info, ok := c.metadata(ctx)
		if !ok || info.BindingID == "" {
			return c.build(ctx, cfg)
		}
		key += "|" + info.BindingID
	}
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if ok && time.Since(e.createdAt) < c.ttl {
		return e.client, nil
	}
	client, err := c.build(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	// Concurrent builders may race, but all callers share the winning entry.
	if existing, ok := c.entries[key]; ok && time.Since(existing.createdAt) < c.ttl {
		client = existing.client
	} else {
		c.entries[key] = entry{client: client, createdAt: time.Now()}
	}
	c.mu.Unlock()
	return client, nil
}
