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
	"k8s.io/apimachinery/pkg/types"
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

	namespace, err := prov.ResolveCreateNamespace(ingress.Namespace, ResourceTypeIngress)
	if err != nil {
		return nil, err
	}

	result, err := ing.Client.NetworkingV1().Ingresses(namespace).Apply(ctx, ingress, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply ingress: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.IngressApplyConfiguration](result, "Ingress", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get ingress live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      ing.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ing *Ingress) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := ing.Client.NetworkingV1().Ingresses(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get ingress: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.IngressApplyConfiguration](result, "Ingress", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get ingress live state: %w", err)
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

	namespace, err := prov.ResolveCreateNamespace(ingress.Namespace, ResourceTypeIngress)
	if err != nil {
		return nil, err
	}

	result, err := ing.Client.NetworkingV1().Ingresses(namespace).Apply(ctx, ingress, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply ingress: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, ingress, func(name string, patch []byte) error {
		_, err := ing.Client.NetworkingV1().Ingresses(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile ingress metadata: %w", err)
	}

	properties, err := prov.LiveState[networkingv1ac.IngressApplyConfiguration](result, "Ingress", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get ingress live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			StatusMessage:      ing.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ing *Ingress) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)

	err := ing.Client.NetworkingV1().Ingresses(ns).Delete(ctx, name, metav1.DeleteOptions{})
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
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := ing.Client.NetworkingV1().Ingresses(ns).Get(ctx, name, metav1.GetOptions{})
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

	properties, err := prov.LiveState[networkingv1ac.IngressApplyConfiguration](result, "Ingress", "networking.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get ingress live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			StatusMessage:      ing.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ing *Ingress) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeIngress)
	if err != nil {
		return nil, err
	}

	result, err := ing.Client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list ingresses: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, ingress := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(ingress.Namespace, ingress.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}


// statusMessage builds a human-readable message from Ingress state.
// The returned message is only rendered by formae for non-Success states,
// which Ingress never produces — it is kept for logging/observability.
func (ing *Ingress) statusMessage(i *networkingv1.Ingress) string {
	if len(i.Status.LoadBalancer.Ingress) > 0 {
		lb := i.Status.LoadBalancer.Ingress[0]
		if lb.IP != "" {
			return fmt.Sprintf("load balancer IP: %s", lb.IP)
		}
		if lb.Hostname != "" {
			return fmt.Sprintf("load balancer hostname: %s", lb.Hostname)
		}
	}
	return "load balancer assigned"
}
