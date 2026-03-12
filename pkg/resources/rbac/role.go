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

const ResourceTypeRole = "K8S::Rbac::Role"

func init() {
	registry.Register(
		ResourceTypeRole,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Role{Client: client, Config: cfg}
		},
	)
}

// Role implements the provisioner for K8S::Rbac::Role resources.
type Role struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Role{}

func (r *Role) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var role *rbacv1ac.RoleApplyConfiguration
	if err := json.Unmarshal(request.Properties, &role); err != nil {
		return nil, fmt.Errorf("failed to unmarshal role properties: %w", err)
	}

	namespace := r.Config.EffectiveNamespace()
	if role.Namespace != nil {
		namespace = *role.Namespace
	}

	result, err := r.Client.RbacV1().Roles(namespace).Apply(ctx, role, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply role: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.RoleApplyConfiguration](result, "Role", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get role live state: %w", err)
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

func (r *Role) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := r.Client.RbacV1().Roles(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get role: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.RoleApplyConfiguration](result, "Role", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get role live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (r *Role) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var role *rbacv1ac.RoleApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &role); err != nil {
		return nil, fmt.Errorf("failed to unmarshal role properties: %w", err)
	}

	namespace := r.Config.EffectiveNamespace()
	if role.Namespace != nil {
		namespace = *role.Namespace
	}

	result, err := r.Client.RbacV1().Roles(namespace).Apply(ctx, role, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply role: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, role, func(name string, patch []byte) error {
		_, err := r.Client.RbacV1().Roles(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile role metadata: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.RoleApplyConfiguration](result, "Role", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get role live state: %w", err)
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

func (r *Role) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)

	err := r.Client.RbacV1().Roles(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete role: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (r *Role) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := r.Client.RbacV1().Roles(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get role status: %w", err)
	}

	properties, err := prov.LiveState[rbacv1ac.RoleApplyConfiguration](result, "Role", "rbac.authorization.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get role live state: %w", err)
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

func (r *Role) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := r.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := r.Client.RbacV1().Roles(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list roles: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, role := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(role.Namespace, role.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

