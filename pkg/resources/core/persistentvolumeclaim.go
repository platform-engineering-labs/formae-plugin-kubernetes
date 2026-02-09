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

const ResourceTypePersistentVolumeClaim = "K8S::Core::PersistentVolumeClaim"

func init() {
	registry.Register(
		ResourceTypePersistentVolumeClaim,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &PersistentVolumeClaim{Client: client, Config: cfg}
		},
	)
}

// PersistentVolumeClaim implements the provisioner for K8S::Core::PersistentVolumeClaim resources.
type PersistentVolumeClaim struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &PersistentVolumeClaim{}

func (p *PersistentVolumeClaim) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var pvc *v1coreac.PersistentVolumeClaimApplyConfiguration
	if err := json.Unmarshal(request.Properties, &pvc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal persistentvolumeclaim properties: %w", err)
	}

	namespace := p.Config.EffectiveNamespace()
	if pvc.Namespace != nil {
		namespace = *pvc.Namespace
	}

	result, err := p.Client.CoreV1().PersistentVolumeClaims(namespace).Apply(ctx, pvc, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply persistentvolumeclaim: %w", err)
	}

	ext, err := v1coreac.ExtractPersistentVolumeClaim(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract persistentvolumeclaim: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal persistentvolumeclaim properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      p.statusMessage(result.Status.Phase),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *PersistentVolumeClaim) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := p.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get persistentvolumeclaim: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := v1coreac.ExtractPersistentVolumeClaim(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract persistentvolumeclaim: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal persistentvolumeclaim properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (p *PersistentVolumeClaim) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var pvc *v1coreac.PersistentVolumeClaimApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &pvc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal persistentvolumeclaim properties: %w", err)
	}

	namespace := p.Config.EffectiveNamespace()
	if pvc.Namespace != nil {
		namespace = *pvc.Namespace
	}

	result, err := p.Client.CoreV1().PersistentVolumeClaims(namespace).Apply(ctx, pvc, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply persistentvolumeclaim: %w", err)
	}

	ext, err := v1coreac.ExtractPersistentVolumeClaim(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract persistentvolumeclaim: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal persistentvolumeclaim properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          result.ResourceVersion,
			StatusMessage:      p.statusMessage(result.Status.Phase),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *PersistentVolumeClaim) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	pvc, err := p.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find persistentvolumeclaim: %w", err)
	}
	if pvc == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = p.Client.CoreV1().PersistentVolumeClaims(pvc.Namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete persistentvolumeclaim: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (p *PersistentVolumeClaim) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
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
		return nil, fmt.Errorf("failed to get persistentvolumeclaim status: %w", err)
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

	ext, err := v1coreac.ExtractPersistentVolumeClaim(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract persistentvolumeclaim: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal persistentvolumeclaim properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          request.RequestID,
			StatusMessage:      p.statusMessage(result.Status.Phase),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *PersistentVolumeClaim) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := p.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := p.Client.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list persistentvolumeclaims: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, pvc := range result.Items {
		nativeIDs = append(nativeIDs, string(pvc.ObjectMeta.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a persistentvolumeclaim by its UID across all namespaces.
func (p *PersistentVolumeClaim) findByUID(ctx context.Context, uid string) (*v1.PersistentVolumeClaim, error) {
	list, err := p.Client.CoreV1().PersistentVolumeClaims(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
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

// fromPhase maps K8S PersistentVolumeClaimPhase to Formae OperationStatus.
// Pending is a normal state for PVCs using WaitForFirstConsumer StorageClasses,
// so we treat it as Success rather than InProgress.
func (p *PersistentVolumeClaim) fromPhase(phase v1.PersistentVolumeClaimPhase) resource.OperationStatus {
	switch phase {
	case v1.ClaimLost:
		return resource.OperationStatusFailure
	default:
		return resource.OperationStatusSuccess
	}
}

// statusMessage returns a human-readable status message for the PVC phase.
func (p *PersistentVolumeClaim) statusMessage(phase v1.PersistentVolumeClaimPhase) string {
	return fmt.Sprintf("phase: %s", phase)
}
