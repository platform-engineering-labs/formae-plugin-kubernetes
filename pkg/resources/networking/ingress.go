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

const ResourceTypeIngress = "K8S::Networking::Ingress"

func init() {
	registry.Register(
		ResourceTypeIngress,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Ingress{Client: client, Config: cfg}
		},
	)
}

// Ingress implements the provisioner for K8S::Networking::Ingress resources.
type Ingress struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Ingress{}

func (ing *Ingress) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var ingress *networkingv1ac.IngressApplyConfiguration
	if err := json.Unmarshal(request.Properties, &ingress); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ingress properties: %w", err)
	}

	namespace := ing.Config.EffectiveNamespace()
	if ingress.Namespace != nil {
		namespace = *ingress.Namespace
	}

	result, err := ing.Client.NetworkingV1().Ingresses(namespace).Apply(ctx, ingress, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply ingress: %w", err)
	}

	ext, err := networkingv1ac.ExtractIngress(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract ingress: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ingress properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    ing.operationStatus(result),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      ing.statusMessage(result),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (ing *Ingress) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := ing.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get ingress: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := networkingv1ac.ExtractIngress(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract ingress: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ingress properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (ing *Ingress) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var ingress *networkingv1ac.IngressApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &ingress); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ingress properties: %w", err)
	}

	namespace := ing.Config.EffectiveNamespace()
	if ingress.Namespace != nil {
		namespace = *ingress.Namespace
	}

	result, err := ing.Client.NetworkingV1().Ingresses(namespace).Apply(ctx, ingress, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply ingress: %w", err)
	}

	ext, err := networkingv1ac.ExtractIngress(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract ingress: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ingress properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    ing.operationStatus(result),
			RequestID:          result.ResourceVersion,
			StatusMessage:      ing.statusMessage(result),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (ing *Ingress) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ingress, err := ing.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find ingress: %w", err)
	}
	if ingress == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = ing.Client.NetworkingV1().Ingresses(ingress.Namespace).Delete(ctx, ingress.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete ingress: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (ing *Ingress) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := ing.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get ingress status: %w", err)
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

	ext, err := networkingv1ac.ExtractIngress(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract ingress: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ingress properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    ing.operationStatus(result),
			RequestID:          request.RequestID,
			StatusMessage:      ing.statusMessage(result),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (ing *Ingress) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := ing.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := ing.Client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list ingresses: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, ingress := range result.Items {
		nativeIDs = append(nativeIDs, string(ingress.ObjectMeta.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds an ingress by its UID across all namespaces.
func (ing *Ingress) findByUID(ctx context.Context, uid string) (*networkingv1.Ingress, error) {
	list, err := ing.Client.NetworkingV1().Ingresses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
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

// operationStatus maps Ingress state to Formae OperationStatus.
// InProgress until a load balancer address is assigned.
func (ing *Ingress) operationStatus(i *networkingv1.Ingress) resource.OperationStatus {
	if len(i.Status.LoadBalancer.Ingress) == 0 {
		return resource.OperationStatusInProgress
	}
	return resource.OperationStatusSuccess
}

// statusMessage builds a status message from Ingress state.
func (ing *Ingress) statusMessage(i *networkingv1.Ingress) string {
	if len(i.Status.LoadBalancer.Ingress) == 0 {
		return "waiting for load balancer address"
	}
	lbIngress := i.Status.LoadBalancer.Ingress[0]
	if lbIngress.IP != "" {
		return fmt.Sprintf("load balancer IP: %s", lbIngress.IP)
	}
	if lbIngress.Hostname != "" {
		return fmt.Sprintf("load balancer hostname: %s", lbIngress.Hostname)
	}
	return "load balancer assigned"
}
