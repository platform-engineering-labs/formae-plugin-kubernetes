// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"context"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// Provisioner defines the interface for K8S resource provisioners.
// Each resource type (Namespace, Pod, etc.) implements this interface.
type Provisioner interface {
	Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error)
	Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error)
	Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error)
	Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error)
	Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error)
	List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error)
}
