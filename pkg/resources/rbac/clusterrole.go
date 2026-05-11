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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply clusterrole: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.ClusterRoleApplyConfiguration](result, "ClusterRole", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get clusterrole live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           result.Name,
			ResourceProperties: properties,
		},
	}, nil
}

func (c *ClusterRole) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := c.Client.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get clusterrole: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.ClusterRoleApplyConfiguration](result, "ClusterRole", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get clusterrole live state: %w", err)
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
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply clusterrole: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, cr, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := c.Client.RbacV1().ClusterRoles().Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile clusterrole metadata: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.ClusterRoleApplyConfiguration](result, "ClusterRole", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get clusterrole live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           result.Name,
			ResourceProperties: properties,
		},
	}, nil
}

func (c *ClusterRole) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = c.Client.RbacV1().ClusterRoles().Delete(ctx, name, metav1.DeleteOptions{})
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
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := c.Client.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
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

	properties, err := prov.LiveState[rbacv1ac.ClusterRoleApplyConfiguration](result, "ClusterRole", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get clusterrole live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           result.Name,
			ResourceProperties: properties,
		},
	}, nil
}

func (c *ClusterRole) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := c.Client.RbacV1().ClusterRoles().List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, cr := range page.Items {
			// System ClusterRoles (`system:*`) are excluded at the plugin
			// level via DiscoveryFilters() in k8s.go — see review H-DSC-1.
			nativeIDs = append(nativeIDs, cr.Name)
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list clusterroles: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
