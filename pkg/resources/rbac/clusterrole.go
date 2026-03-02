// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package rbac

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	rbacv1ac "k8s.io/client-go/applyconfigurations/rbac/v1"
)

const ResourceTypeClusterRole = "K8S::Rbac::ClusterRole"

func init() {
	registry.Register(
		ResourceTypeClusterRole,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &ClusterRole{Client: client, Config: cfg}
		},
	)
}

// ClusterRole implements the provisioner for K8S::Rbac::ClusterRole resources.
type ClusterRole struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &ClusterRole{}

func (c *ClusterRole) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var cr *rbacv1ac.ClusterRoleApplyConfiguration
	if err := json.Unmarshal(request.Properties, &cr); err != nil {
		return nil, fmt.Errorf("failed to unmarshal clusterrole properties: %w", err)
	}

	result, err := c.Client.RbacV1().ClusterRoles().Apply(ctx, cr, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply clusterrole: %w", err)
	}

	ext, err := rbacv1ac.ExtractClusterRole(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract clusterrole: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal clusterrole properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (c *ClusterRole) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := c.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get clusterrole: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := rbacv1ac.ExtractClusterRole(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract clusterrole: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal clusterrole properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (c *ClusterRole) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var cr *rbacv1ac.ClusterRoleApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &cr); err != nil {
		return nil, fmt.Errorf("failed to unmarshal clusterrole properties: %w", err)
	}

	result, err := c.Client.RbacV1().ClusterRoles().Apply(ctx, cr, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply clusterrole: %w", err)
	}

	ext, err := rbacv1ac.ExtractClusterRole(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract clusterrole: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal clusterrole properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (c *ClusterRole) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	cr, err := c.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find clusterrole: %w", err)
	}
	if cr == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = c.Client.RbacV1().ClusterRoles().Delete(ctx, cr.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete clusterrole: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (c *ClusterRole) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := c.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.StatusResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationCheckStatus,
					OperationStatus: resource.OperationStatusFailure,
					ErrorCode:       resource.OperationErrorCodeNotFound,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to get clusterrole status: %w", err)
	}
	if result == nil {
		return &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCheckStatus,
				OperationStatus: resource.OperationStatusFailure,
				ErrorCode:       resource.OperationErrorCodeNotFound,
			},
		}, nil
	}

	ext, err := rbacv1ac.ExtractClusterRole(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract clusterrole: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal clusterrole properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (c *ClusterRole) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := c.Client.RbacV1().ClusterRoles().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list clusterroles: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, cr := range result.Items {
		nativeIDs = append(nativeIDs, string(cr.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a clusterrole by its UID.
// K8S doesn't support metadata.uid field selector for ClusterRoles,
// so we list all and filter in memory.
// Returns nil if not found (no error).
func (c *ClusterRole) findByUID(ctx context.Context, uid string) (*rbacv1.ClusterRole, error) {
	list, err := c.Client.RbacV1().ClusterRoles().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if string(list.Items[i].UID) == uid {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}
