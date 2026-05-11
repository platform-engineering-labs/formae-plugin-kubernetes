// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package coordination

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
	coordinationv1ac "k8s.io/client-go/applyconfigurations/coordination/v1"
)

const ResourceTypeLease = "K8S::Coordination::Lease"

func init() {
	registry.Register(
		ResourceTypeLease,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Lease{Client: client, Config: cfg}
		},
	)
}

// Lease implements the provisioner for K8S::Coordination::Lease resources.
type Lease struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Lease{}

func (l *Lease) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var lease *coordinationv1ac.LeaseApplyConfiguration
	if err := json.Unmarshal(request.Properties, &lease); err != nil {
		return nil, fmt.Errorf("failed to unmarshal lease properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(lease.Namespace, ResourceTypeLease)
	if err != nil {
		return nil, err
	}

	result, err := l.Client.CoordinationV1().Leases(namespace).Apply(ctx, lease, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply lease: %w", err)
	}

	properties, err := prov.LiveState[coordinationv1ac.LeaseApplyConfiguration](result, "Lease", "coordination.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get lease live state: %w", err)
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

func (l *Lease) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := l.Client.CoordinationV1().Leases(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get lease: %w", err)
	}

	properties, err := prov.LiveState[coordinationv1ac.LeaseApplyConfiguration](result, "Lease", "coordination.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get lease live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (l *Lease) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var lease *coordinationv1ac.LeaseApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &lease); err != nil {
		return nil, fmt.Errorf("failed to unmarshal lease properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(lease.Namespace, ResourceTypeLease)
	if err != nil {
		return nil, err
	}

	result, err := l.Client.CoordinationV1().Leases(namespace).Apply(ctx, lease, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply lease: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, lease, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := l.Client.CoordinationV1().Leases(namespace).Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile lease metadata: %w", err)
	}

	properties, err := prov.LiveState[coordinationv1ac.LeaseApplyConfiguration](result, "Lease", "coordination.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get lease live state: %w", err)
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

func (l *Lease) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = l.Client.CoordinationV1().Leases(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete lease: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (l *Lease) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := l.Client.CoordinationV1().Leases(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get lease status: %w", err)
	}

	properties, err := prov.LiveState[coordinationv1ac.LeaseApplyConfiguration](result, "Lease", "coordination.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get lease live state: %w", err)
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

func (l *Lease) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeLease)
	if err != nil {
		return nil, err
	}

	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := l.Client.CoordinationV1().Leases(namespace).List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, lease := range page.Items {
			nativeIDs = append(nativeIDs, prov.NativeID(lease.Namespace, lease.Name))
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list leases: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
