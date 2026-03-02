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
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	namespace := n.Config.EffectiveNamespace()
	if np.Namespace != nil {
		namespace = *np.Namespace
	}

	result, err := n.Client.NetworkingV1().NetworkPolicies(namespace).Apply(ctx, np, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply networkpolicy: %w", err)
	}

	ext, err := networkingv1ac.ExtractNetworkPolicy(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract networkpolicy: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal networkpolicy properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (n *NetworkPolicy) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := n.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get networkpolicy: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := networkingv1ac.ExtractNetworkPolicy(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract networkpolicy: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal networkpolicy properties: %w", err)
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

	namespace := n.Config.EffectiveNamespace()
	if np.Namespace != nil {
		namespace = *np.Namespace
	}

	result, err := n.Client.NetworkingV1().NetworkPolicies(namespace).Apply(ctx, np, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply networkpolicy: %w", err)
	}

	ext, err := networkingv1ac.ExtractNetworkPolicy(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract networkpolicy: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal networkpolicy properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (n *NetworkPolicy) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	np, err := n.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find networkpolicy: %w", err)
	}
	if np == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = n.Client.NetworkingV1().NetworkPolicies(np.Namespace).Delete(ctx, np.Name, metav1.DeleteOptions{})
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
	result, err := n.findByUID(ctx, request.NativeID)
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
	if result == nil {
		return &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCheckStatus,
				OperationStatus: resource.OperationStatusFailure,
				ErrorCode:       resource.OperationErrorCodeNotFound,
			},
		}, nil
	}

	ext, err := networkingv1ac.ExtractNetworkPolicy(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract networkpolicy: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal networkpolicy properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (n *NetworkPolicy) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := n.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := n.Client.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list networkpolicies: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, np := range result.Items {
		nativeIDs = append(nativeIDs, string(np.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a networkpolicy by its UID across all namespaces.
func (n *NetworkPolicy) findByUID(ctx context.Context, uid string) (*networkingv1.NetworkPolicy, error) {
	list, err := n.Client.NetworkingV1().NetworkPolicies(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
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
