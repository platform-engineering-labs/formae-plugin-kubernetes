// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	v1coreac "k8s.io/client-go/applyconfigurations/core/v1"
)

const ResourceTypeService = "K8S::Core::Service"

func init() {
	registry.Register(
		ResourceTypeService,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Service{Client: client, Config: cfg}
		},
	)
}

// Service implements the provisioner for K8S::Core::Service resources.
type Service struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Service{}

func (svc *Service) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var s *v1coreac.ServiceApplyConfiguration
	if err := json.Unmarshal(request.Properties, &s); err != nil {
		return nil, fmt.Errorf("failed to unmarshal service properties: %w", err)
	}

	namespace := svc.Config.EffectiveNamespace()
	if s.Namespace != nil {
		namespace = *s.Namespace
	}

	result, err := svc.Client.CoreV1().Services(namespace).Apply(ctx, s, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply service: %w", err)
	}

	ext, err := v1coreac.ExtractService(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract service: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal service properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    svc.operationStatus(result),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      svc.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (svc *Service) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := svc.Client.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get service: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.ServiceApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get service live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (svc *Service) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var s *v1coreac.ServiceApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &s); err != nil {
		return nil, fmt.Errorf("failed to unmarshal service properties: %w", err)
	}

	namespace := svc.Config.EffectiveNamespace()
	if s.Namespace != nil {
		namespace = *s.Namespace
	}

	result, err := svc.Client.CoreV1().Services(namespace).Apply(ctx, s, metav1.ApplyOptions{
		FieldManager: "formae",
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply service: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, s, func(name string, patch []byte) error {
		_, err := svc.Client.CoreV1().Services(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile service metadata: %w", err)
	}

	ext, err := v1coreac.ExtractService(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract service: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal service properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    svc.operationStatus(result),
			RequestID:          result.ResourceVersion,
			StatusMessage:      svc.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (svc *Service) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := svc.Client.CoreV1().Services(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete service: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (svc *Service) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := svc.Client.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get service status: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.ServiceApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get service live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    svc.operationStatus(result),
			RequestID:          request.RequestID,
			StatusMessage:      svc.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (svc *Service) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := svc.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := svc.Client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, s := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(s.Namespace, s.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// operationStatus maps Service state to Formae OperationStatus.
// When WaitForLoadBalancer is enabled, LoadBalancer services report InProgress
// until an external IP is assigned. Otherwise returns Success immediately.
func (svc *Service) operationStatus(s *v1.Service) resource.OperationStatus {
	if svc.Config.ShouldWaitForLoadBalancer() && s.Spec.Type == v1.ServiceTypeLoadBalancer {
		if len(s.Status.LoadBalancer.Ingress) == 0 {
			return resource.OperationStatusInProgress
		}
	}
	return resource.OperationStatusSuccess
}

// statusMessage builds a status message from Service state.
func (svc *Service) statusMessage(s *v1.Service) string {
	if s.Spec.Type == v1.ServiceTypeLoadBalancer {
		if len(s.Status.LoadBalancer.Ingress) == 0 {
			return "waiting for load balancer IP"
		}
		ingress := s.Status.LoadBalancer.Ingress[0]
		if ingress.IP != "" {
			return fmt.Sprintf("load balancer IP: %s", ingress.IP)
		}
		if ingress.Hostname != "" {
			return fmt.Sprintf("load balancer hostname: %s", ingress.Hostname)
		}
	}
	return fmt.Sprintf("type: %s, clusterIP: %s", s.Spec.Type, s.Spec.ClusterIP)
}
