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

	namespace := rb.Config.EffectiveNamespace()
	if binding.Namespace != nil {
		namespace = *binding.Namespace
	}

	result, err := rb.Client.RbacV1().RoleBindings(namespace).Apply(ctx, binding, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply rolebinding: %w", err)
	}

	ext, err := rbacv1ac.ExtractRoleBinding(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract rolebinding: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rolebinding properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (rb *RoleBinding) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
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

	ext, err := rbacv1ac.ExtractRoleBinding(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract rolebinding: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rolebinding properties: %w", err)
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

	namespace := rb.Config.EffectiveNamespace()
	if binding.Namespace != nil {
		namespace = *binding.Namespace
	}

	result, err := rb.Client.RbacV1().RoleBindings(namespace).Apply(ctx, binding, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply rolebinding: %w", err)
	}

	ext, err := rbacv1ac.ExtractRoleBinding(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract rolebinding: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rolebinding properties: %w", err)
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
	ns, name := prov.ParseNativeID(request.NativeID)

	err := rb.Client.RbacV1().RoleBindings(ns).Delete(ctx, name, metav1.DeleteOptions{})
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
	ns, name := prov.ParseNativeID(request.NativeID)
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

	ext, err := rbacv1ac.ExtractRoleBinding(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract rolebinding: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rolebinding properties: %w", err)
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
	namespace := rb.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := rb.Client.RbacV1().RoleBindings(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list rolebindings: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, binding := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(binding.Namespace, binding.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

