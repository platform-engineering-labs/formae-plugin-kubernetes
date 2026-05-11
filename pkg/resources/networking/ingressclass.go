// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package networking

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
	networkingv1ac "k8s.io/client-go/applyconfigurations/networking/v1"
)

const ResourceTypeIngressClass = "K8S::Networking::IngressClass"

func init() {
	registry.Register(
		ResourceTypeIngressClass,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &IngressClass{Client: client, Config: cfg}
		},
	)
}

// IngressClass implements the provisioner for K8S::Networking::IngressClass resources.
type IngressClass struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &IngressClass{}

func (ic *IngressClass) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var ingressClass *networkingv1ac.IngressClassApplyConfiguration
	if err := json.Unmarshal(request.Properties, &ingressClass); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ingressclass properties: %w", err)
	}

	result, err := ic.Client.NetworkingV1().IngressClasses().Apply(ctx, ingressClass, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply ingressclass: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.IngressClassApplyConfiguration](result, "IngressClass", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get ingressclass live state: %w", err)
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

func (ic *IngressClass) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := ic.Client.NetworkingV1().IngressClasses().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get ingressclass: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.IngressClassApplyConfiguration](result, "IngressClass", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get ingressclass live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (ic *IngressClass) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var ingressClass *networkingv1ac.IngressClassApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &ingressClass); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ingressclass properties: %w", err)
	}

	result, err := ic.Client.NetworkingV1().IngressClasses().Apply(ctx, ingressClass, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply ingressclass: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, ingressClass, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := ic.Client.NetworkingV1().IngressClasses().Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile ingressclass metadata: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.IngressClassApplyConfiguration](result, "IngressClass", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get ingressclass live state: %w", err)
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

func (ic *IngressClass) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = ic.Client.NetworkingV1().IngressClasses().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete ingressclass: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (ic *IngressClass) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	name, err := prov.ParseClusterNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := ic.Client.NetworkingV1().IngressClasses().Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get ingressclass status: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.IngressClassApplyConfiguration](result, "IngressClass", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get ingressclass live state: %w", err)
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

func (ic *IngressClass) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := ic.Client.NetworkingV1().IngressClasses().List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, ingressClass := range page.Items {
			nativeIDs = append(nativeIDs, ingressClass.Name)
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list ingressclasses: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
