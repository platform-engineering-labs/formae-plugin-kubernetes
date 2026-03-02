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

const ResourceTypePersistentVolume = "K8S::Core::PersistentVolume"

func init() {
	registry.Register(
		ResourceTypePersistentVolume,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &PersistentVolume{Client: client, Config: cfg}
		},
	)
}

// PersistentVolume implements the provisioner for K8S::Core::PersistentVolume resources.
type PersistentVolume struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &PersistentVolume{}

func (p *PersistentVolume) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var pv *v1coreac.PersistentVolumeApplyConfiguration
	if err := json.Unmarshal(request.Properties, &pv); err != nil {
		return nil, fmt.Errorf("failed to unmarshal persistentvolume properties: %w", err)
	}

	result, err := p.Client.CoreV1().PersistentVolumes().Apply(ctx, pv, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply persistentvolume: %w", err)
	}

	ext, err := v1coreac.ExtractPersistentVolume(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract persistentvolume: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal persistentvolume properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      p.statusMessage(result.Status.Phase),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *PersistentVolume) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := p.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get persistentvolume: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := v1coreac.ExtractPersistentVolume(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract persistentvolume: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal persistentvolume properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (p *PersistentVolume) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var pv *v1coreac.PersistentVolumeApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &pv); err != nil {
		return nil, fmt.Errorf("failed to unmarshal persistentvolume properties: %w", err)
	}

	result, err := p.Client.CoreV1().PersistentVolumes().Apply(ctx, pv, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply persistentvolume: %w", err)
	}

	ext, err := v1coreac.ExtractPersistentVolume(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract persistentvolume: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal persistentvolume properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          result.ResourceVersion,
			StatusMessage:      p.statusMessage(result.Status.Phase),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *PersistentVolume) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	pv, err := p.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find persistentvolume: %w", err)
	}
	if pv == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = p.Client.CoreV1().PersistentVolumes().Delete(ctx, pv.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete persistentvolume: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (p *PersistentVolume) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
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
		return nil, fmt.Errorf("failed to get persistentvolume status: %w", err)
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

	ext, err := v1coreac.ExtractPersistentVolume(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract persistentvolume: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal persistentvolume properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    p.fromPhase(result.Status.Phase),
			RequestID:          request.RequestID,
			StatusMessage:      p.statusMessage(result.Status.Phase),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (p *PersistentVolume) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := p.Client.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list persistentvolumes: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, pv := range result.Items {
		nativeIDs = append(nativeIDs, string(pv.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a persistentvolume by its UID.
// PersistentVolumes are cluster-scoped, so no namespace filtering is needed.
func (p *PersistentVolume) findByUID(ctx context.Context, uid string) (*v1.PersistentVolume, error) {
	list, err := p.Client.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
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

// fromPhase maps K8S PersistentVolumePhase to Formae OperationStatus.
func (p *PersistentVolume) fromPhase(phase v1.PersistentVolumePhase) resource.OperationStatus {
	switch phase {
	case v1.VolumeFailed:
		return resource.OperationStatusFailure
	default:
		return resource.OperationStatusSuccess
	}
}

// statusMessage returns a human-readable status message for the PV phase.
func (p *PersistentVolume) statusMessage(phase v1.PersistentVolumePhase) string {
	return fmt.Sprintf("phase: %s", phase)
}
