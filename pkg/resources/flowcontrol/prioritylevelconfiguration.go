// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package flowcontrol

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
	flowcontrolv1ac "k8s.io/client-go/applyconfigurations/flowcontrol/v1"
)

const ResourceTypePriorityLevelConfiguration = "K8S::Flowcontrol::PriorityLevelConfiguration"

func init() {
	registry.Register(
		ResourceTypePriorityLevelConfiguration,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &PriorityLevelConfiguration{Client: client, Config: cfg}
		},
	)
}

// PriorityLevelConfiguration implements the provisioner for K8S::Flowcontrol::PriorityLevelConfiguration resources.
type PriorityLevelConfiguration struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &PriorityLevelConfiguration{}

func (p *PriorityLevelConfiguration) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var plc *flowcontrolv1ac.PriorityLevelConfigurationApplyConfiguration
	if err := json.Unmarshal(request.Properties, &plc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal prioritylevelconfiguration properties: %w", err)
	}

	result, err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().Apply(ctx, plc, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply prioritylevelconfiguration: %w", err)
	}

	ext, err := flowcontrolv1ac.ExtractPriorityLevelConfiguration(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract prioritylevelconfiguration: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal prioritylevelconfiguration properties: %w", err)
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

func (p *PriorityLevelConfiguration) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get prioritylevelconfiguration: %w", err)
	}

	properties, err := prov.LiveState[flowcontrolv1ac.PriorityLevelConfigurationApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get prioritylevelconfiguration live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (p *PriorityLevelConfiguration) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var plc *flowcontrolv1ac.PriorityLevelConfigurationApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &plc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal prioritylevelconfiguration properties: %w", err)
	}

	result, err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().Apply(ctx, plc, metav1.ApplyOptions{
		FieldManager: "formae",
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply prioritylevelconfiguration: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, plc, func(name string, patch []byte) error {
		_, err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile prioritylevelconfiguration metadata: %w", err)
	}

	ext, err := flowcontrolv1ac.ExtractPriorityLevelConfiguration(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract prioritylevelconfiguration: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal prioritylevelconfiguration properties: %w", err)
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

func (p *PriorityLevelConfiguration) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete prioritylevelconfiguration: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (p *PriorityLevelConfiguration) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get prioritylevelconfiguration status: %w", err)
	}

	properties, err := prov.LiveState[flowcontrolv1ac.PriorityLevelConfigurationApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get prioritylevelconfiguration live state: %w", err)
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

func (p *PriorityLevelConfiguration) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list prioritylevelconfigurations: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, plc := range result.Items {
		nativeIDs = append(nativeIDs, plc.Name)
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
