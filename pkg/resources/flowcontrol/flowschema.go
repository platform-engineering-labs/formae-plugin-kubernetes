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
	flowcontrolv1ac "k8s.io/client-go/applyconfigurations/flowcontrol/v1"
)

const ResourceTypeFlowSchema = "K8S::Flowcontrol::FlowSchema"

func init() {
	registry.Register(
		ResourceTypeFlowSchema,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &FlowSchema{Client: client, Config: cfg}
		},
	)
}

// FlowSchema implements the provisioner for K8S::Flowcontrol::FlowSchema resources.
type FlowSchema struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &FlowSchema{}

func (f *FlowSchema) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var fs *flowcontrolv1ac.FlowSchemaApplyConfiguration
	if err := json.Unmarshal(request.Properties, &fs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal flowschema properties: %w", err)
	}

	result, err := f.Client.FlowcontrolV1().FlowSchemas().Apply(ctx, fs, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply flowschema: %w", err)
	}

	ext, err := flowcontrolv1ac.ExtractFlowSchema(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract flowschema: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal flowschema properties: %w", err)
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

func (f *FlowSchema) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := f.Client.FlowcontrolV1().FlowSchemas().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get flowschema: %w", err)
	}

	ext, err := flowcontrolv1ac.ExtractFlowSchema(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract flowschema: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal flowschema properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (f *FlowSchema) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var fs *flowcontrolv1ac.FlowSchemaApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &fs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal flowschema properties: %w", err)
	}

	result, err := f.Client.FlowcontrolV1().FlowSchemas().Apply(ctx, fs, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply flowschema: %w", err)
	}

	ext, err := flowcontrolv1ac.ExtractFlowSchema(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract flowschema: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal flowschema properties: %w", err)
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

func (f *FlowSchema) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	err := f.Client.FlowcontrolV1().FlowSchemas().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete flowschema: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (f *FlowSchema) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := f.Client.FlowcontrolV1().FlowSchemas().Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get flowschema status: %w", err)
	}

	ext, err := flowcontrolv1ac.ExtractFlowSchema(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract flowschema: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal flowschema properties: %w", err)
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

func (f *FlowSchema) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := f.Client.FlowcontrolV1().FlowSchemas().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list flowschemas: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, fs := range result.Items {
		nativeIDs = append(nativeIDs, fs.Name)
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
