// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package admissionregistration

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
	admissionregistrationv1ac "k8s.io/client-go/applyconfigurations/admissionregistration/v1"
)

const ResourceTypeValidatingWebhookConfiguration = "K8S::Admissionregistration::ValidatingWebhookConfiguration"

func init() {
	registry.Register(
		ResourceTypeValidatingWebhookConfiguration,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &ValidatingWebhookConfiguration{Client: client, Config: cfg}
		},
	)
}

// ValidatingWebhookConfiguration implements the provisioner for K8S::Admissionregistration::ValidatingWebhookConfiguration resources.
type ValidatingWebhookConfiguration struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &ValidatingWebhookConfiguration{}

func (v *ValidatingWebhookConfiguration) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var vwc *admissionregistrationv1ac.ValidatingWebhookConfigurationApplyConfiguration
	if err := json.Unmarshal(request.Properties, &vwc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal validatingwebhookconfiguration properties: %w", err)
	}

	result, err := v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Apply(ctx, vwc, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply validatingwebhookconfiguration: %w", err)
	}

	ext, err := admissionregistrationv1ac.ExtractValidatingWebhookConfiguration(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract validatingwebhookconfiguration: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal validatingwebhookconfiguration properties: %w", err)
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

func (v *ValidatingWebhookConfiguration) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get validatingwebhookconfiguration: %w", err)
	}

	ext, err := admissionregistrationv1ac.ExtractValidatingWebhookConfiguration(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract validatingwebhookconfiguration: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal validatingwebhookconfiguration properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (v *ValidatingWebhookConfiguration) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var vwc *admissionregistrationv1ac.ValidatingWebhookConfigurationApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &vwc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal validatingwebhookconfiguration properties: %w", err)
	}

	result, err := v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Apply(ctx, vwc, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply validatingwebhookconfiguration: %w", err)
	}

	ext, err := admissionregistrationv1ac.ExtractValidatingWebhookConfiguration(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract validatingwebhookconfiguration: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal validatingwebhookconfiguration properties: %w", err)
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

func (v *ValidatingWebhookConfiguration) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	err := v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete validatingwebhookconfiguration: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (v *ValidatingWebhookConfiguration) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get validatingwebhookconfiguration status: %w", err)
	}

	ext, err := admissionregistrationv1ac.ExtractValidatingWebhookConfiguration(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract validatingwebhookconfiguration: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal validatingwebhookconfiguration properties: %w", err)
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

func (v *ValidatingWebhookConfiguration) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list validatingwebhookconfigurations: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, vwc := range result.Items {
		nativeIDs = append(nativeIDs, vwc.Name)
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
