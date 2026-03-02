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
	v1coreac "k8s.io/client-go/applyconfigurations/core/v1"
)

const ResourceTypePod = "K8S::Core::Pod"

func init() {
	registry.Register(
		ResourceTypePod,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Pod{Client: client, Config: cfg}
		},
	)
}

// Pod implements the provisioner for K8S::Pod resources.
type Pod struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Pod{}

func (p *Pod) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var pod *v1coreac.PodApplyConfiguration
	if err := json.Unmarshal(request.Properties, &pod); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pod properties: %w", err)
	}

	namespace := p.Config.EffectiveNamespace()
	if pod.Namespace != nil {
		namespace = *pod.Namespace
	}

	result, err := p.Client.CoreV1().Pods(namespace).Apply(ctx, pod, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply pod: %w", err)
	}

	// Extract only the fields managed by formae
	ext, err := v1coreac.ExtractPod(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract pod: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pod properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      result.Status.Message,
			ErrorCode:          p.fromReason(result.Status.Reason),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *Pod) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	// K8S API needs name, but we have UID. Use field selector to find by UID.
	result, err := p.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get pod: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	// Extract only the fields managed by formae
	ext, err := v1coreac.ExtractPod(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract pod: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pod properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (p *Pod) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var pod *v1coreac.PodApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &pod); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pod properties: %w", err)
	}

	namespace := p.Config.EffectiveNamespace()
	if pod.Namespace != nil {
		namespace = *pod.Namespace
	}

	result, err := p.Client.CoreV1().Pods(namespace).Apply(ctx, pod, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply pod: %w", err)
	}

	// Extract only the fields managed by formae
	ext, err := v1coreac.ExtractPod(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract pod: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pod properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          result.ResourceVersion,
			StatusMessage:      result.Status.Message,
			ErrorCode:          p.fromReason(result.Status.Reason),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *Pod) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	// Find pod by UID first to get the name and namespace
	pod, err := p.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find pod: %w", err)
	}
	if pod == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = p.Client.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete pod: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (p *Pod) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := p.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get pod status: %w", err)
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

	// Extract only the fields managed by formae
	ext, err := v1coreac.ExtractPod(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract pod: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pod properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          request.RequestID,
			StatusMessage:      result.Status.Message,
			ErrorCode:          p.fromReason(result.Status.Reason),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *Pod) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := p.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := p.Client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, pod := range result.Items {
		nativeIDs = append(nativeIDs, string(pod.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a pod by its UID.
// K8S doesn't universally support metadata.uid field selector,
// so we list all pods across all namespaces and filter in memory.
// We search all namespaces because discovery may find resources outside
// the configured default namespace.
func (p *Pod) findByUID(ctx context.Context, uid string) (*v1.Pod, error) {
	list, err := p.Client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
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

// fromPhase maps K8S PodPhase to Formae OperationStatus.
func (p *Pod) fromPhase(phase v1.PodPhase) resource.OperationStatus {
	switch phase {
	case v1.PodRunning, v1.PodSucceeded:
		return resource.OperationStatusSuccess
	case v1.PodPending:
		return resource.OperationStatusInProgress
	case v1.PodFailed:
		return resource.OperationStatusFailure
	case v1.PodUnknown:
		return resource.OperationStatusInProgress
	default:
		return resource.OperationStatusSuccess
	}
}

// fromReason maps K8S pod reason to Formae error code.
func (p *Pod) fromReason(reason string) resource.OperationErrorCode {
	switch reason {
	case "Evicted":
		return resource.OperationErrorCodeResourceConflict
	case "OutOfMemory", "OutOfCpu", "OutOfDisk":
		return resource.OperationErrorCodeServiceLimitExceeded
	default:
		return ""
	}
}
