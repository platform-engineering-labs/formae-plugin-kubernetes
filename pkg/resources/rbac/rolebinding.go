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

const ResourceTypeRoleBinding = "K8S::Rbac::RoleBinding"

func init() {
	registry.Register(
		ResourceTypeRoleBinding,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &RoleBinding{Client: client, Config: cfg}
		},
	)
}

// RoleBinding implements the provisioner for K8S::Rbac::RoleBinding resources.
type RoleBinding struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &RoleBinding{}

func (rb *RoleBinding) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var binding *rbacv1ac.RoleBindingApplyConfiguration
	if err := json.Unmarshal(request.Properties, &binding); err != nil {
		return nil, fmt.Errorf("failed to unmarshal rolebinding properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(binding.Namespace, ResourceTypeRoleBinding)
	if err != nil {
		return nil, err
	}

	result, err := rb.Client.RbacV1().RoleBindings(namespace).Apply(ctx, binding, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply rolebinding: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.RoleBindingApplyConfiguration](result, "RoleBinding", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get rolebinding live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (rb *RoleBinding) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := rb.Client.RbacV1().RoleBindings(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get rolebinding: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.RoleBindingApplyConfiguration](result, "RoleBinding", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get rolebinding live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (rb *RoleBinding) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var binding *rbacv1ac.RoleBindingApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &binding); err != nil {
		return nil, fmt.Errorf("failed to unmarshal rolebinding properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(binding.Namespace, ResourceTypeRoleBinding)
	if err != nil {
		return nil, err
	}

	result, err := rb.Client.RbacV1().RoleBindings(namespace).Apply(ctx, binding, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply rolebinding: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, binding, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := rb.Client.RbacV1().RoleBindings(namespace).Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile rolebinding metadata: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.RoleBindingApplyConfiguration](result, "RoleBinding", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get rolebinding live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (rb *RoleBinding) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}

	err = rb.Client.RbacV1().RoleBindings(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete rolebinding: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (rb *RoleBinding) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := rb.Client.RbacV1().RoleBindings(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get rolebinding status: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.RoleBindingApplyConfiguration](result, "RoleBinding", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get rolebinding live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (rb *RoleBinding) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeRoleBinding)
	if err != nil {
		return nil, err
	}

	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := rb.Client.RbacV1().RoleBindings(namespace).List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, binding := range page.Items {
			nativeIDs = append(nativeIDs, prov.NativeID(binding.Namespace, binding.Name))
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list rolebindings: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

