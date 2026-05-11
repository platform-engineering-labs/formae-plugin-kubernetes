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

const ResourceTypeClusterRoleBinding = "K8S::Rbac::ClusterRoleBinding"

func init() {
	registry.Register(
		ResourceTypeClusterRoleBinding,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &ClusterRoleBinding{Client: client, Config: cfg}
		},
	)
}

// ClusterRoleBinding implements the provisioner for K8S::Rbac::ClusterRoleBinding resources.
type ClusterRoleBinding struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &ClusterRoleBinding{}

func (c *ClusterRoleBinding) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var crb *rbacv1ac.ClusterRoleBindingApplyConfiguration
	if err := json.Unmarshal(request.Properties, &crb); err != nil {
		return nil, fmt.Errorf("failed to unmarshal clusterrolebinding properties: %w", err)
	}

	result, err := c.Client.RbacV1().ClusterRoleBindings().Apply(ctx, crb, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply clusterrolebinding: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.ClusterRoleBindingApplyConfiguration](result, "ClusterRoleBinding", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get clusterrolebinding live state: %w", err)
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

func (c *ClusterRoleBinding) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := c.Client.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get clusterrolebinding: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.ClusterRoleBindingApplyConfiguration](result, "ClusterRoleBinding", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get clusterrolebinding live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (c *ClusterRoleBinding) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var crb *rbacv1ac.ClusterRoleBindingApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &crb); err != nil {
		return nil, fmt.Errorf("failed to unmarshal clusterrolebinding properties: %w", err)
	}

	result, err := c.Client.RbacV1().ClusterRoleBindings().Apply(ctx, crb, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply clusterrolebinding: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, crb, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := c.Client.RbacV1().ClusterRoleBindings().Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile clusterrolebinding metadata: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.ClusterRoleBindingApplyConfiguration](result, "ClusterRoleBinding", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get clusterrolebinding live state: %w", err)
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

func (c *ClusterRoleBinding) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = c.Client.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete clusterrolebinding: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (c *ClusterRoleBinding) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := c.Client.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get clusterrolebinding status: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.ClusterRoleBindingApplyConfiguration](result, "ClusterRoleBinding", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get clusterrolebinding live state: %w", err)
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

func (c *ClusterRoleBinding) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := c.Client.RbacV1().ClusterRoleBindings().List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, crb := range page.Items {
			// System ClusterRoleBindings (`system:*`) are excluded at the
			// plugin level via DiscoveryFilters() in k8s.go — see review H-DSC-1.
			nativeIDs = append(nativeIDs, crb.Name)
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list clusterrolebindings: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
