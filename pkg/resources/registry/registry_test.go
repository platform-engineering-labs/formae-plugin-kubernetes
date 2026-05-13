// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package registry

import (
	"context"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noopProv struct{}

func (n *noopProv) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, nil
}
func (n *noopProv) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	return nil, nil
}
func (n *noopProv) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, nil
}
func (n *noopProv) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return nil, nil
}
func (n *noopProv) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	return nil, nil
}
func (n *noopProv) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	return nil, nil
}

func TestRegister_PanicsOnDuplicate(t *testing.T) {
	const rt = "K8S::Test::Duplicate"

	factory := func(_ *transport.Client, _ *config.Config) prov.Provisioner {
		return &noopProv{}
	}

	// First registration succeeds.
	Register(rt, []resource.Operation{resource.OperationRead}, factory)

	// Second registration of the same resource type must panic.
	assert.PanicsWithValue(t,
		`duplicate registration for resource type "K8S::Test::Duplicate"`,
		func() {
			Register(rt, []resource.Operation{resource.OperationRead}, factory)
		},
	)

	// Cleanup to keep the global registry tidy for other tests.
	mu.Lock()
	delete(registrations, rt)
	mu.Unlock()
}

func TestRegister_DistinctTypesCoexist(t *testing.T) {
	const a = "K8S::Test::Alpha"
	const b = "K8S::Test::Beta"

	factory := func(_ *transport.Client, _ *config.Config) prov.Provisioner {
		return &noopProv{}
	}

	Register(a, []resource.Operation{resource.OperationRead}, factory)
	require.NotPanics(t, func() {
		Register(b, []resource.Operation{resource.OperationRead}, factory)
	})

	assert.True(t, HasProvisioner(a))
	assert.True(t, HasProvisioner(b))

	mu.Lock()
	delete(registrations, a)
	delete(registrations, b)
	mu.Unlock()
}
