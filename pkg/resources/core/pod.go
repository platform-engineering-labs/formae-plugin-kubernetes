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

	if err := prov.CheckPayloadGates(ctx, ResourceTypePod, request.Properties, p.Client, p.Config); err != nil {
		return nil, err
	}

	namespace := "default"
	if pod.Namespace != nil {
		namespace = *pod.Namespace
	}

	result, err := p.Client.CoreV1().Pods(namespace).Apply(ctx, pod, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply pod: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.PodApplyConfiguration](result, "Pod", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get pod live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      p.statusMessage(result),
			ErrorCode:          p.fromReason(result.Status.Reason),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *Pod) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := p.Client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get pod: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.PodApplyConfiguration](result, "Pod", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get pod live state: %w", err)
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

	if err := prov.CheckPayloadGates(ctx, ResourceTypePod, request.DesiredProperties, p.Client, p.Config); err != nil {
		return nil, err
	}

	namespace := "default"
	if pod.Namespace != nil {
		namespace = *pod.Namespace
	}

	result, err := p.Client.CoreV1().Pods(namespace).Apply(ctx, pod, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply pod: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, pod, func(name string, patch []byte) error {
		_, err := p.Client.CoreV1().Pods(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile pod metadata: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.PodApplyConfiguration](result, "Pod", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get pod live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          result.ResourceVersion,
			StatusMessage:      p.statusMessage(result),
			ErrorCode:          p.fromReason(result.Status.Reason),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *Pod) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := p.Client.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{})
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
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := p.Client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
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

	properties, err := prov.LiveState[v1coreac.PodApplyConfiguration](result, "Pod", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get pod live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          request.RequestID,
			StatusMessage:      p.statusMessage(result),
			ErrorCode:          p.fromReason(result.Status.Reason),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *Pod) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := "default"
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := p.Client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, pod := range result.Items {
		// Skip pods owned by a controller (ReplicaSet, DaemonSet, Job, etc.)
		// Only discover standalone pods that were created directly.
		if len(pod.OwnerReferences) > 0 {
			continue
		}
		nativeIDs = append(nativeIDs, prov.NativeID(pod.Namespace, pod.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
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

// statusMessage builds a human-readable message from pod status, including
// container-level warnings (CrashLoopBackOff, ImagePullBackOff, etc.) that
// are invisible at the pod phase level.
func (p *Pod) statusMessage(pod *v1.Pod) string {
	msg := fmt.Sprintf("phase: %s", pod.Status.Phase)

	var warnings []string
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			switch cs.State.Waiting.Reason {
			case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull",
				"CreateContainerConfigError", "InvalidImageName":
				warnings = append(warnings, fmt.Sprintf("container %s: %s", cs.Name, cs.State.Waiting.Reason))
			}
		}
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil {
			switch cs.State.Waiting.Reason {
			case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull",
				"CreateContainerConfigError", "InvalidImageName":
				warnings = append(warnings, fmt.Sprintf("init container %s: %s", cs.Name, cs.State.Waiting.Reason))
			}
		}
	}

	if len(warnings) > 0 {
		for _, w := range warnings {
			msg += fmt.Sprintf(" (WARNING: %s)", w)
		}
	}
	return msg
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
