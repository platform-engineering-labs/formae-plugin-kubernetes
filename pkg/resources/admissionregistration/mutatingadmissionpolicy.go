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

const ResourceTypeMutatingAdmissionPolicy = "K8S::Admissionregistration::MutatingAdmissionPolicy"

func init() {
	registry.Register(
		ResourceTypeMutatingAdmissionPolicy,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &MutatingAdmissionPolicy{Client: client, Config: cfg}
		},
	)
}

// MutatingAdmissionPolicy implements the provisioner for K8S::Admissionregistration::MutatingAdmissionPolicy resources.
type MutatingAdmissionPolicy struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &MutatingAdmissionPolicy{}

func (m *MutatingAdmissionPolicy) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var mp *admissionregistrationv1ac.MutatingAdmissionPolicyApplyConfiguration
	if err := prov.UnmarshalApplyConfig(request.Properties, &mp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mutatingadmissionpolicy properties: %w", err)
	}

	result, err := m.Client.AdmissionregistrationV1().MutatingAdmissionPolicies().Apply(ctx, mp, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply mutatingadmissionpolicy: %w", err)
	}

	properties, err := prov.LiveState[admissionregistrationv1ac.MutatingAdmissionPolicyApplyConfiguration](result, "MutatingAdmissionPolicy", "admissionregistration.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get mutatingadmissionpolicy live state: %w", err)
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

func (m *MutatingAdmissionPolicy) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := m.Client.AdmissionregistrationV1().MutatingAdmissionPolicies().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get mutatingadmissionpolicy: %w", err)
	}

	properties, err := prov.LiveState[admissionregistrationv1ac.MutatingAdmissionPolicyApplyConfiguration](result, "MutatingAdmissionPolicy", "admissionregistration.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get mutatingadmissionpolicy live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (m *MutatingAdmissionPolicy) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var mp *admissionregistrationv1ac.MutatingAdmissionPolicyApplyConfiguration
	if err := prov.UnmarshalApplyConfig(request.DesiredProperties, &mp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mutatingadmissionpolicy properties: %w", err)
	}

	result, err := m.Client.AdmissionregistrationV1().MutatingAdmissionPolicies().Apply(ctx, mp, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply mutatingadmissionpolicy: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, mp, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := m.Client.AdmissionregistrationV1().MutatingAdmissionPolicies().Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile mutatingadmissionpolicy metadata: %w", err)
	}

	properties, err := prov.LiveState[admissionregistrationv1ac.MutatingAdmissionPolicyApplyConfiguration](result, "MutatingAdmissionPolicy", "admissionregistration.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get mutatingadmissionpolicy live state: %w", err)
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

func (m *MutatingAdmissionPolicy) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = m.Client.AdmissionregistrationV1().MutatingAdmissionPolicies().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete mutatingadmissionpolicy: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (m *MutatingAdmissionPolicy) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := m.Client.AdmissionregistrationV1().MutatingAdmissionPolicies().Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get mutatingadmissionpolicy status: %w", err)
	}

	properties, err := prov.LiveState[admissionregistrationv1ac.MutatingAdmissionPolicyApplyConfiguration](result, "MutatingAdmissionPolicy", "admissionregistration.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get mutatingadmissionpolicy live state: %w", err)
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

func (m *MutatingAdmissionPolicy) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := m.Client.AdmissionregistrationV1().MutatingAdmissionPolicies().List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, mp := range page.Items {
			nativeIDs = append(nativeIDs, mp.Name)
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list mutatingadmissionpolicies: %w", err)
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
