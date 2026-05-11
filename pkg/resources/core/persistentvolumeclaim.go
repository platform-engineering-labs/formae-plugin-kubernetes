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

	namespace, err := prov.ResolveCreateNamespace(pvc.Namespace, ResourceTypePersistentVolumeClaim)
	if err != nil {
		return nil, err
	}

	result, err := p.Client.CoreV1().PersistentVolumeClaims(namespace).Apply(ctx, pvc, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply persistentvolumeclaim: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.PersistentVolumeClaimApplyConfiguration](result, "PersistentVolumeClaim", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get persistentvolumeclaim live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      p.statusMessage(result.Status.Phase),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *PersistentVolumeClaim) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := p.Client.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get persistentvolumeclaim: %w", err)
	}

	// A Lost PVC's backing PV was deleted — the claim can never bind again.
	if result.Status.Phase == v1.ClaimLost {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	properties, err := prov.LiveState[v1coreac.PersistentVolumeClaimApplyConfiguration](result, "PersistentVolumeClaim", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get persistentvolumeclaim live state: %w", err)
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

	namespace, err := prov.ResolveCreateNamespace(pvc.Namespace, ResourceTypePersistentVolumeClaim)
	if err != nil {
		return nil, err
	}

	result, err := p.Client.CoreV1().PersistentVolumeClaims(namespace).Apply(ctx, pvc, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply persistentvolumeclaim: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, pvc, func(name string, patch []byte) error {
		_, err := p.Client.CoreV1().PersistentVolumeClaims(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile persistentvolumeclaim metadata: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.PersistentVolumeClaimApplyConfiguration](result, "PersistentVolumeClaim", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get persistentvolumeclaim live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          result.ResourceVersion,
			StatusMessage:      p.statusMessage(result.Status.Phase),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *PersistentVolumeClaim) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := p.Client.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, metav1.DeleteOptions{})
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
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := p.Client.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
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

	properties, err := prov.LiveState[v1coreac.PersistentVolumeClaimApplyConfiguration](result, "PersistentVolumeClaim", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get persistentvolumeclaim live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          request.RequestID,
			StatusMessage:      p.statusMessage(result.Status.Phase),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *PersistentVolumeClaim) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypePersistentVolumeClaim)
	if err != nil {
		return nil, err
	}

	result, err := p.Client.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list persistentvolumeclaims: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, pvc := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(pvc.Namespace, pvc.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
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
