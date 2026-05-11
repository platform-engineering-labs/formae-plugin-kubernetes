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

const ResourceTypeNetworkPolicy = "K8S::Networking::NetworkPolicy"

func init() {
	registry.Register(
		ResourceTypeNetworkPolicy,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &NetworkPolicy{Client: client, Config: cfg}
		},
	)
}

// NetworkPolicy implements the provisioner for K8S::Networking::NetworkPolicy resources.
type NetworkPolicy struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &NetworkPolicy{}

func (n *NetworkPolicy) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var np *networkingv1ac.NetworkPolicyApplyConfiguration
	if err := json.Unmarshal(request.Properties, &np); err != nil {
		return nil, fmt.Errorf("failed to unmarshal networkpolicy properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(np.Namespace, ResourceTypeNetworkPolicy)
	if err != nil {
		return nil, err
	}

	result, err := n.Client.NetworkingV1().NetworkPolicies(namespace).Apply(ctx, np, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply networkpolicy: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.NetworkPolicyApplyConfiguration](result, "NetworkPolicy", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get networkpolicy live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (n *NetworkPolicy) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := n.Client.NetworkingV1().NetworkPolicies(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get networkpolicy: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.NetworkPolicyApplyConfiguration](result, "NetworkPolicy", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get networkpolicy live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (n *NetworkPolicy) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var np *networkingv1ac.NetworkPolicyApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &np); err != nil {
		return nil, fmt.Errorf("failed to unmarshal networkpolicy properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(np.Namespace, ResourceTypeNetworkPolicy)
	if err != nil {
		return nil, err
	}

	result, err := n.Client.NetworkingV1().NetworkPolicies(namespace).Apply(ctx, np, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply networkpolicy: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, np, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := n.Client.NetworkingV1().NetworkPolicies(namespace).Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile networkpolicy metadata: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.NetworkPolicyApplyConfiguration](result, "NetworkPolicy", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get networkpolicy live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (n *NetworkPolicy) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}

	err = n.Client.NetworkingV1().NetworkPolicies(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete networkpolicy: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (n *NetworkPolicy) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := n.Client.NetworkingV1().NetworkPolicies(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get networkpolicy status: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.NetworkPolicyApplyConfiguration](result, "NetworkPolicy", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get networkpolicy live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (n *NetworkPolicy) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeNetworkPolicy)
	if err != nil {
		return nil, err
	}

	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := n.Client.NetworkingV1().NetworkPolicies(namespace).List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, np := range page.Items {
			nativeIDs = append(nativeIDs, prov.NativeID(np.Namespace, np.Name))
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list networkpolicies: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

