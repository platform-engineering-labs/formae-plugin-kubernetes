// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"sync"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// ProvisionerFactory creates a provisioner for a K8S client and config.
type ProvisionerFactory func(client *transport.Client, cfg *config.Config) prov.Provisioner

type registration struct {
	factory    ProvisionerFactory
	operations []resource.Operation
}

var (
	mu            sync.RWMutex
	registrations = make(map[string]*registration)
)

// Register registers a resource type with its provisioner factory.
// The factory is automatically wrapped with lifecycle-aware behavior
// that detects terminating K8S objects (deletionTimestamp set).
func Register(resourceType string, operations []resource.Operation, factory ProvisionerFactory) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registrations[resourceType]; exists {
		panic(fmt.Sprintf("duplicate registration for resource type %q", resourceType))
	}
	wrappedFactory := func(client *transport.Client, cfg *config.Config) prov.Provisioner {
		return prov.Wrap(factory(client, cfg))
	}
	registrations[resourceType] = &registration{
		factory:    wrappedFactory,
		operations: operations,
	}
}

// GetFactory returns the provisioner factory for a resource type.
func GetFactory(resourceType string) (ProvisionerFactory, bool) {
	mu.RLock()
	defer mu.RUnlock()
	reg, ok := registrations[resourceType]
	if !ok {
		return nil, false
	}
	return reg.factory, true
}

// GetOperations returns the supported operations for a resource type.
func GetOperations(resourceType string) []resource.Operation {
	mu.RLock()
	defer mu.RUnlock()
	reg, ok := registrations[resourceType]
	if !ok {
		return nil
	}
	return reg.operations
}

// HasProvisioner checks if a resource type is registered.
func HasProvisioner(resourceType string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registrations[resourceType]
	return ok
}

// ResourceTypes returns all registered resource types.
func ResourceTypes() []string {
	mu.RLock()
	defer mu.RUnlock()
	types := make([]string, 0, len(registrations))
	for t := range registrations {
		types = append(types, t)
	}
	return types
}
