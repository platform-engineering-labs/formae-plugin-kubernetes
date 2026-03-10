// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package autoscaling

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	autoscalingv2ac "k8s.io/client-go/applyconfigurations/autoscaling/v2"
)

const ResourceTypeHorizontalPodAutoscaler = "K8S::Autoscaling::HorizontalPodAutoscaler"

func init() {
	registry.Register(
		ResourceTypeHorizontalPodAutoscaler,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &HorizontalPodAutoscaler{Client: client, Config: cfg}
		},
	)
}

// HorizontalPodAutoscaler implements the provisioner for K8S::Autoscaling::HorizontalPodAutoscaler resources.
type HorizontalPodAutoscaler struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &HorizontalPodAutoscaler{}

func (h *HorizontalPodAutoscaler) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var hpa *autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration
	if err := json.Unmarshal(request.Properties, &hpa); err != nil {
		return nil, fmt.Errorf("failed to unmarshal horizontalpodautoscaler properties: %w", err)
	}

	namespace := h.Config.EffectiveNamespace()
	if hpa.Namespace != nil {
		namespace = *hpa.Namespace
	}

	result, err := h.Client.AutoscalingV2().HorizontalPodAutoscalers(namespace).Apply(ctx, hpa, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply horizontalpodautoscaler: %w", err)
	}

	ext, err := autoscalingv2ac.ExtractHorizontalPodAutoscaler(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract horizontalpodautoscaler: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal horizontalpodautoscaler properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    h.fromConditions(result),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      h.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (h *HorizontalPodAutoscaler) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := h.Client.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get horizontalpodautoscaler: %w", err)
	}

	properties, err := prov.LiveState[autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get horizontalpodautoscaler live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (h *HorizontalPodAutoscaler) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var hpa *autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &hpa); err != nil {
		return nil, fmt.Errorf("failed to unmarshal horizontalpodautoscaler properties: %w", err)
	}

	namespace := h.Config.EffectiveNamespace()
	if hpa.Namespace != nil {
		namespace = *hpa.Namespace
	}

	result, err := h.Client.AutoscalingV2().HorizontalPodAutoscalers(namespace).Apply(ctx, hpa, metav1.ApplyOptions{
		FieldManager: "formae",
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply horizontalpodautoscaler: %w", err)
	}

	ext, err := autoscalingv2ac.ExtractHorizontalPodAutoscaler(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract horizontalpodautoscaler: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal horizontalpodautoscaler properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    h.fromConditions(result),
			RequestID:          result.ResourceVersion,
			StatusMessage:      h.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (h *HorizontalPodAutoscaler) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := h.Client.AutoscalingV2().HorizontalPodAutoscalers(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete horizontalpodautoscaler: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (h *HorizontalPodAutoscaler) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := h.Client.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get horizontalpodautoscaler status: %w", err)
	}

	properties, err := prov.LiveState[autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get horizontalpodautoscaler live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    h.fromConditions(result),
			RequestID:          request.RequestID,
			StatusMessage:      h.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (h *HorizontalPodAutoscaler) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := h.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := h.Client.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list horizontalpodautoscalers: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, hpa := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(hpa.Namespace, hpa.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// fromConditions maps HPA conditions to Formae OperationStatus.
// HPA is a declarative configuration object — once applied, it is considered
// successful unless the controller reports it is unable to scale at all.
func (h *HorizontalPodAutoscaler) fromConditions(hpa *autoscalingv2.HorizontalPodAutoscaler) resource.OperationStatus {
	for _, cond := range hpa.Status.Conditions {
		if cond.Type == autoscalingv2.AbleToScale && cond.Status == "False" {
			return resource.OperationStatusFailure
		}
	}
	return resource.OperationStatusSuccess
}

// statusMessage builds a status message from HPA status.
func (h *HorizontalPodAutoscaler) statusMessage(hpa *autoscalingv2.HorizontalPodAutoscaler) string {
	return fmt.Sprintf("currentReplicas: %d, desiredReplicas: %d",
		hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas)
}
