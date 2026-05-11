// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package core

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
	v1coreac "k8s.io/client-go/applyconfigurations/core/v1"
)

const ResourceTypeEndpoints = "K8S::Core::Endpoints"

func init() {
	registry.Register(
		ResourceTypeEndpoints,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Endpoints{Client: client, Config: cfg}
		},
	)
}

// Endpoints implements the provisioner for K8S::Core::Endpoints resources.
type Endpoints struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Endpoints{}

func (e *Endpoints) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var ep *v1coreac.EndpointsApplyConfiguration //nolint:staticcheck // Endpoints resource intentionally supported
	if err := json.Unmarshal(request.Properties, &ep); err != nil {
		return nil, fmt.Errorf("failed to unmarshal endpoints properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(ep.Namespace, ResourceTypeEndpoints)
	if err != nil {
		return nil, err
	}

	result, err := e.Client.CoreV1().Endpoints(namespace).Apply(ctx, ep, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply endpoints: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.EndpointsApplyConfiguration](result, "Endpoints", "v1") //nolint:staticcheck // migrating to EndpointSlice tracked separately
	if err != nil {
		return nil, fmt.Errorf("failed to get endpoints live state: %w", err)
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

func (e *Endpoints) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := e.Client.CoreV1().Endpoints(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get endpoints: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.EndpointsApplyConfiguration](result, "Endpoints", "v1") //nolint:staticcheck // migrating to EndpointSlice tracked separately
	if err != nil {
		return nil, fmt.Errorf("failed to get endpoints live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (e *Endpoints) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var ep *v1coreac.EndpointsApplyConfiguration //nolint:staticcheck // Endpoints resource intentionally supported
	if err := json.Unmarshal(request.DesiredProperties, &ep); err != nil {
		return nil, fmt.Errorf("failed to unmarshal endpoints properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(ep.Namespace, ResourceTypeEndpoints)
	if err != nil {
		return nil, err
	}

	result, err := e.Client.CoreV1().Endpoints(namespace).Apply(ctx, ep, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply endpoints: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, ep, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := e.Client.CoreV1().Endpoints(namespace).Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile endpoints metadata: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.EndpointsApplyConfiguration](result, "Endpoints", "v1") //nolint:staticcheck // migrating to EndpointSlice tracked separately
	if err != nil {
		return nil, fmt.Errorf("failed to get endpoints live state: %w", err)
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

func (e *Endpoints) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = e.Client.CoreV1().Endpoints(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete endpoints: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (e *Endpoints) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := e.Client.CoreV1().Endpoints(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get endpoints status: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.EndpointsApplyConfiguration](result, "Endpoints", "v1") //nolint:staticcheck // migrating to EndpointSlice tracked separately
	if err != nil {
		return nil, fmt.Errorf("failed to get endpoints live state: %w", err)
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

func (e *Endpoints) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeEndpoints)
	if err != nil {
		return nil, err
	}

	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := e.Client.CoreV1().Endpoints(namespace).List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, ep := range page.Items {
			nativeIDs = append(nativeIDs, prov.NativeID(ep.Namespace, ep.Name))
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list endpoints: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
