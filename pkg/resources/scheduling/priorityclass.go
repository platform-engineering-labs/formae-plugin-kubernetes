// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package scheduling

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
	schedulingv1ac "k8s.io/client-go/applyconfigurations/scheduling/v1"
)

const ResourceTypePriorityClass = "K8S::Scheduling::PriorityClass"

func init() {
	registry.Register(
		ResourceTypePriorityClass,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &PriorityClass{Client: client, Config: cfg}
		},
	)
}

// PriorityClass implements the provisioner for K8S::Scheduling::PriorityClass resources.
type PriorityClass struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &PriorityClass{}

func (pc *PriorityClass) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var priorityClass *schedulingv1ac.PriorityClassApplyConfiguration
	if err := json.Unmarshal(request.Properties, &priorityClass); err != nil {
		return nil, fmt.Errorf("failed to unmarshal priorityclass properties: %w", err)
	}

	result, err := pc.Client.SchedulingV1().PriorityClasses().Apply(ctx, priorityClass, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply priorityclass: %w", err)
	}

	properties, err := prov.LiveState[schedulingv1ac.PriorityClassApplyConfiguration](result, "PriorityClass", "scheduling.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get priorityclass live state: %w", err)
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

func (pc *PriorityClass) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := pc.Client.SchedulingV1().PriorityClasses().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get priorityclass: %w", err)
	}

	properties, err := prov.LiveState[schedulingv1ac.PriorityClassApplyConfiguration](result, "PriorityClass", "scheduling.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get priorityclass live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (pc *PriorityClass) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var priorityClass *schedulingv1ac.PriorityClassApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &priorityClass); err != nil {
		return nil, fmt.Errorf("failed to unmarshal priorityclass properties: %w", err)
	}

	result, err := pc.Client.SchedulingV1().PriorityClasses().Apply(ctx, priorityClass, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply priorityclass: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, priorityClass, func(name string, patch []byte) error {
		_, err := pc.Client.SchedulingV1().PriorityClasses().Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile priorityclass metadata: %w", err)
	}

	properties, err := prov.LiveState[schedulingv1ac.PriorityClassApplyConfiguration](result, "PriorityClass", "scheduling.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get priorityclass live state: %w", err)
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

func (pc *PriorityClass) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	err := pc.Client.SchedulingV1().PriorityClasses().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete priorityclass: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (pc *PriorityClass) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := pc.Client.SchedulingV1().PriorityClasses().Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get priorityclass status: %w", err)
	}

	properties, err := prov.LiveState[schedulingv1ac.PriorityClassApplyConfiguration](result, "PriorityClass", "scheduling.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get priorityclass live state: %w", err)
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

func (pc *PriorityClass) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := pc.Client.SchedulingV1().PriorityClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list priorityclasses: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, priorityClass := range result.Items {
		nativeIDs = append(nativeIDs, priorityClass.Name)
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
