// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package node

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
	nodev1ac "k8s.io/client-go/applyconfigurations/node/v1"
)

const ResourceTypeRuntimeClass = "K8S::Node::RuntimeClass"

func init() {
	registry.Register(
		ResourceTypeRuntimeClass,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &RuntimeClass{Client: client, Config: cfg}
		},
	)
}

// RuntimeClass implements the provisioner for K8S::Node::RuntimeClass resources.
type RuntimeClass struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &RuntimeClass{}

func (r *RuntimeClass) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var rc *nodev1ac.RuntimeClassApplyConfiguration
	if err := json.Unmarshal(request.Properties, &rc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal runtimeclass properties: %w", err)
	}

	result, err := r.Client.NodeV1().RuntimeClasses().Apply(ctx, rc, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply runtimeclass: %w", err)
	}

	properties, err := prov.LiveState[nodev1ac.RuntimeClassApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get runtimeclass live state: %w", err)
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

func (r *RuntimeClass) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := r.Client.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get runtimeclass: %w", err)
	}

	properties, err := prov.LiveState[nodev1ac.RuntimeClassApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get runtimeclass live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (r *RuntimeClass) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var rc *nodev1ac.RuntimeClassApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &rc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal runtimeclass properties: %w", err)
	}

	result, err := r.Client.NodeV1().RuntimeClasses().Apply(ctx, rc, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply runtimeclass: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, rc, func(name string, patch []byte) error {
		_, err := r.Client.NodeV1().RuntimeClasses().Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile runtimeclass metadata: %w", err)
	}

	properties, err := prov.LiveState[nodev1ac.RuntimeClassApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get runtimeclass live state: %w", err)
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

func (r *RuntimeClass) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	err := r.Client.NodeV1().RuntimeClasses().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete runtimeclass: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (r *RuntimeClass) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := r.Client.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get runtimeclass status: %w", err)
	}

	properties, err := prov.LiveState[nodev1ac.RuntimeClassApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get runtimeclass live state: %w", err)
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

func (r *RuntimeClass) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := r.Client.NodeV1().RuntimeClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list runtimeclasses: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, rc := range result.Items {
		nativeIDs = append(nativeIDs, rc.Name)
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
