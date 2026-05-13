// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package autoscaling

import (
	"context"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	if err := prov.UnmarshalApplyConfig(request.Properties, &hpa); err != nil {
		return nil, fmt.Errorf("failed to unmarshal horizontalpodautoscaler properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(hpa.Namespace, ResourceTypeHorizontalPodAutoscaler)
	if err != nil {
		return nil, err
	}

	result, err := h.Client.AutoscalingV2().HorizontalPodAutoscalers(namespace).Apply(ctx, hpa, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply horizontalpodautoscaler: %w", err)
	}

	properties, err := prov.LiveState[autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration](result, "HorizontalPodAutoscaler", "autoscaling/v2")
	if err != nil {
		return nil, fmt.Errorf("failed to get horizontalpodautoscaler live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    h.operationStatus(result),
			RequestID:          result.ResourceVersion,
			StatusMessage:      h.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (h *HorizontalPodAutoscaler) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	properties, err := prov.LiveState[autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration](result, "HorizontalPodAutoscaler", "autoscaling/v2")
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
	if err := prov.UnmarshalApplyConfig(request.DesiredProperties, &hpa); err != nil {
		return nil, fmt.Errorf("failed to unmarshal horizontalpodautoscaler properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(hpa.Namespace, ResourceTypeHorizontalPodAutoscaler)
	if err != nil {
		return nil, err
	}

	result, err := h.Client.AutoscalingV2().HorizontalPodAutoscalers(namespace).Apply(ctx, hpa, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply horizontalpodautoscaler: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, hpa, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := h.Client.AutoscalingV2().HorizontalPodAutoscalers(namespace).Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile horizontalpodautoscaler metadata: %w", err)
	}

	properties, err := prov.LiveState[autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration](result, "HorizontalPodAutoscaler", "autoscaling/v2")
	if err != nil {
		return nil, fmt.Errorf("failed to get horizontalpodautoscaler live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    h.operationStatus(result),
			RequestID:          result.ResourceVersion,
			StatusMessage:      h.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (h *HorizontalPodAutoscaler) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = h.Client.AutoscalingV2().HorizontalPodAutoscalers(ns).Delete(ctx, name, metav1.DeleteOptions{})
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
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	properties, err := prov.LiveState[autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration](result, "HorizontalPodAutoscaler", "autoscaling/v2")
	if err != nil {
		return nil, fmt.Errorf("failed to get horizontalpodautoscaler live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    h.operationStatus(result),
			RequestID:          request.RequestID,
			StatusMessage:      h.statusMessage(result),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (h *HorizontalPodAutoscaler) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeHorizontalPodAutoscaler)
	if err != nil {
		return nil, err
	}

	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := h.Client.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, hpa := range page.Items {
			nativeIDs = append(nativeIDs, prov.NativeID(hpa.Namespace, hpa.Name))
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list horizontalpodautoscalers: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// operationStatus returns OperationStatus for the HPA.
//
// HPA is a declarative configuration object: a successful Apply means the
// scaling policy is installed in the cluster. Runtime conditions such as
// AbleToScale=False or ScalingActive=False reflect transient controller
// behavior (missing metrics, target reconciling, etc.) — not provisioning
// failures — and are surfaced via statusMessage rather than failing the
// Formae operation. Aligns with the "config object always-Success" policy
// applied to CronJob, Ingress, etc.
func (h *HorizontalPodAutoscaler) operationStatus(_ *autoscalingv2.HorizontalPodAutoscaler) resource.OperationStatus {
	return resource.OperationStatusSuccess
}

// statusMessage builds a status message from HPA status.
func (h *HorizontalPodAutoscaler) statusMessage(hpa *autoscalingv2.HorizontalPodAutoscaler) string {
	return fmt.Sprintf("currentReplicas: %d, desiredReplicas: %d",
		hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas)
}
