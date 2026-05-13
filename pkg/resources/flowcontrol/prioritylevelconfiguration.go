// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package flowcontrol

import (
	"context"
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
	if err := prov.UnmarshalApplyConfig(request.Properties, &plc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal prioritylevelconfiguration properties: %w", err)
	}

	result, err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().Apply(ctx, plc, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply prioritylevelconfiguration: %w", err)
	}

	properties, err := prov.LiveState[flowcontrolv1ac.PriorityLevelConfigurationApplyConfiguration](result, "PriorityLevelConfiguration", "flowcontrol.apiserver.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get prioritylevelconfiguration live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           result.Name,
			ResourceProperties: properties,
		},
	}, nil
}

func (p *PriorityLevelConfiguration) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	properties, err := prov.LiveState[flowcontrolv1ac.PriorityLevelConfigurationApplyConfiguration](result, "PriorityLevelConfiguration", "flowcontrol.apiserver.k8s.io/v1")
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
	if err := prov.UnmarshalApplyConfig(request.DesiredProperties, &plc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal prioritylevelconfiguration properties: %w", err)
	}

	result, err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().Apply(ctx, plc, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply prioritylevelconfiguration: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, plc, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile prioritylevelconfiguration metadata: %w", err)
	}

	properties, err := prov.LiveState[flowcontrolv1ac.PriorityLevelConfigurationApplyConfiguration](result, "PriorityLevelConfiguration", "flowcontrol.apiserver.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get prioritylevelconfiguration live state: %w", err)
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
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = p.Client.FlowcontrolV1().PriorityLevelConfigurations().Delete(ctx, name, metav1.DeleteOptions{})
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
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	properties, err := prov.LiveState[flowcontrolv1ac.PriorityLevelConfigurationApplyConfiguration](result, "PriorityLevelConfiguration", "flowcontrol.apiserver.k8s.io/v1")
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
	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := p.Client.FlowcontrolV1().PriorityLevelConfigurations().List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, plc := range page.Items {
			nativeIDs = append(nativeIDs, plc.Name)
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list prioritylevelconfigurations: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
