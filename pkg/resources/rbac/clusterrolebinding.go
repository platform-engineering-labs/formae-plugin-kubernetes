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
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply clusterrolebinding: %w", err)
	}

	ext, err := rbacv1ac.ExtractClusterRoleBinding(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract clusterrolebinding: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal clusterrolebinding properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (c *ClusterRoleBinding) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := c.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get clusterrolebinding: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := rbacv1ac.ExtractClusterRoleBinding(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract clusterrolebinding: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal clusterrolebinding properties: %w", err)
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
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply clusterrolebinding: %w", err)
	}

	ext, err := rbacv1ac.ExtractClusterRoleBinding(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract clusterrolebinding: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal clusterrolebinding properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (c *ClusterRoleBinding) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	crb, err := c.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find clusterrolebinding: %w", err)
	}
	if crb == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = c.Client.RbacV1().ClusterRoleBindings().Delete(ctx, crb.Name, metav1.DeleteOptions{})
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
		return nil, fmt.Errorf("failed to get clusterrolebinding status: %w", err)
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

	ext, err := rbacv1ac.ExtractClusterRoleBinding(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract clusterrolebinding: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal clusterrolebinding properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (c *ClusterRoleBinding) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := c.Client.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list clusterrolebindings: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, crb := range result.Items {
		nativeIDs = append(nativeIDs, string(crb.ObjectMeta.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a clusterrolebinding by its UID.
// K8S doesn't support metadata.uid field selector for ClusterRoleBindings,
// so we list all and filter in memory.
// Returns nil if not found (no error).
func (c *ClusterRoleBinding) findByUID(ctx context.Context, uid string) (*rbacv1.ClusterRoleBinding, error) {
	list, err := c.Client.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
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
