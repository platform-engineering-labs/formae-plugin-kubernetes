// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package admissionregistration

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
	if err := prov.UnmarshalApplyConfig(request.Properties, &vwc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal validatingwebhookconfiguration properties: %w", err)
	}

	result, err := v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Apply(ctx, vwc, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply validatingwebhookconfiguration: %w", err)
	}

	properties, err := prov.LiveState[admissionregistrationv1ac.ValidatingWebhookConfigurationApplyConfiguration](result, "ValidatingWebhookConfiguration", "admissionregistration.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get validatingwebhookconfiguration live state: %w", err)
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

func (v *ValidatingWebhookConfiguration) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	properties, err := prov.LiveState[admissionregistrationv1ac.ValidatingWebhookConfigurationApplyConfiguration](result, "ValidatingWebhookConfiguration", "admissionregistration.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get validatingwebhookconfiguration live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (v *ValidatingWebhookConfiguration) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var vwc *admissionregistrationv1ac.ValidatingWebhookConfigurationApplyConfiguration
	if err := prov.UnmarshalApplyConfig(request.DesiredProperties, &vwc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal validatingwebhookconfiguration properties: %w", err)
	}

	result, err := v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Apply(ctx, vwc, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply validatingwebhookconfiguration: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, vwc, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile validatingwebhookconfiguration metadata: %w", err)
	}

	properties, err := prov.LiveState[admissionregistrationv1ac.ValidatingWebhookConfigurationApplyConfiguration](result, "ValidatingWebhookConfiguration", "admissionregistration.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get validatingwebhookconfiguration live state: %w", err)
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
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Delete(ctx, name, metav1.DeleteOptions{})
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
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	properties, err := prov.LiveState[admissionregistrationv1ac.ValidatingWebhookConfigurationApplyConfiguration](result, "ValidatingWebhookConfiguration", "admissionregistration.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get validatingwebhookconfiguration live state: %w", err)
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
	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := v.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, vwc := range page.Items {
			nativeIDs = append(nativeIDs, vwc.Name)
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list validatingwebhookconfigurations: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
