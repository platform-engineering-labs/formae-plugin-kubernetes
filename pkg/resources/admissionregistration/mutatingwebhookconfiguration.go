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
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	admissionregistrationv1ac "k8s.io/client-go/applyconfigurations/admissionregistration/v1"
)

const ResourceTypeMutatingWebhookConfiguration = "K8S::Admissionregistration::MutatingWebhookConfiguration"

func init() {
	registry.Register(
		ResourceTypeMutatingWebhookConfiguration,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &MutatingWebhookConfiguration{Client: client, Config: cfg}
		},
	)
}

// MutatingWebhookConfiguration implements the provisioner for K8S::Admissionregistration::MutatingWebhookConfiguration resources.
type MutatingWebhookConfiguration struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &MutatingWebhookConfiguration{}

func (m *MutatingWebhookConfiguration) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var mwc *admissionregistrationv1ac.MutatingWebhookConfigurationApplyConfiguration
	if err := json.Unmarshal(request.Properties, &mwc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mutatingwebhookconfiguration properties: %w", err)
	}

	result, err := m.Client.AdmissionregistrationV1().MutatingWebhookConfigurations().Apply(ctx, mwc, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply mutatingwebhookconfiguration: %w", err)
	}

	ext, err := admissionregistrationv1ac.ExtractMutatingWebhookConfiguration(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract mutatingwebhookconfiguration: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal mutatingwebhookconfiguration properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (m *MutatingWebhookConfiguration) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := m.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get mutatingwebhookconfiguration: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := admissionregistrationv1ac.ExtractMutatingWebhookConfiguration(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract mutatingwebhookconfiguration: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal mutatingwebhookconfiguration properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (m *MutatingWebhookConfiguration) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var mwc *admissionregistrationv1ac.MutatingWebhookConfigurationApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &mwc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mutatingwebhookconfiguration properties: %w", err)
	}

	result, err := m.Client.AdmissionregistrationV1().MutatingWebhookConfigurations().Apply(ctx, mwc, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply mutatingwebhookconfiguration: %w", err)
	}

	ext, err := admissionregistrationv1ac.ExtractMutatingWebhookConfiguration(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract mutatingwebhookconfiguration: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal mutatingwebhookconfiguration properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (m *MutatingWebhookConfiguration) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	mwc, err := m.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find mutatingwebhookconfiguration: %w", err)
	}
	if mwc == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = m.Client.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(ctx, mwc.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete mutatingwebhookconfiguration: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (m *MutatingWebhookConfiguration) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := m.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get mutatingwebhookconfiguration status: %w", err)
	}
	if result == nil {
		return &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCheckStatus,
				OperationStatus: resource.OperationStatusFailure,
				ErrorCode:       resource.OperationErrorCodeNotFound,
			},
		}, nil
	}

	ext, err := admissionregistrationv1ac.ExtractMutatingWebhookConfiguration(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract mutatingwebhookconfiguration: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal mutatingwebhookconfiguration properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (m *MutatingWebhookConfiguration) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := m.Client.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list mutatingwebhookconfigurations: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, mwc := range result.Items {
		nativeIDs = append(nativeIDs, string(mwc.ObjectMeta.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a MutatingWebhookConfiguration by its UID.
func (m *MutatingWebhookConfiguration) findByUID(ctx context.Context, uid string) (*admissionregistrationv1.MutatingWebhookConfiguration, error) {
	list, err := m.Client.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if string(list.Items[i].UID) == uid {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}
