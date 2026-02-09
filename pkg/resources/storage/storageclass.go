// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	storagev1ac "k8s.io/client-go/applyconfigurations/storage/v1"
)

const ResourceTypeStorageClass = "K8S::Storage::StorageClass"

func init() {
	registry.Register(
		ResourceTypeStorageClass,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &StorageClass{Client: client, Config: cfg}
		},
	)
}

// StorageClass implements the provisioner for K8S::Storage::StorageClass resources.
type StorageClass struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &StorageClass{}

func (s *StorageClass) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var sc *storagev1ac.StorageClassApplyConfiguration
	if err := json.Unmarshal(request.Properties, &sc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal storageclass properties: %w", err)
	}

	result, err := s.Client.StorageV1().StorageClasses().Apply(ctx, sc, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply storageclass: %w", err)
	}

	ext, err := storagev1ac.ExtractStorageClass(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract storageclass: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal storageclass properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (s *StorageClass) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := s.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get storageclass: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := storagev1ac.ExtractStorageClass(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract storageclass: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal storageclass properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (s *StorageClass) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var sc *storagev1ac.StorageClassApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &sc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal storageclass properties: %w", err)
	}

	result, err := s.Client.StorageV1().StorageClasses().Apply(ctx, sc, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply storageclass: %w", err)
	}

	ext, err := storagev1ac.ExtractStorageClass(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract storageclass: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal storageclass properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (s *StorageClass) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	sc, err := s.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find storageclass: %w", err)
	}
	if sc == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = s.Client.StorageV1().StorageClasses().Delete(ctx, sc.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete storageclass: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (s *StorageClass) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := s.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get storageclass status: %w", err)
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

	ext, err := storagev1ac.ExtractStorageClass(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract storageclass: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal storageclass properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (s *StorageClass) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := s.Client.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list storageclasses: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, sc := range result.Items {
		nativeIDs = append(nativeIDs, string(sc.ObjectMeta.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a storageclass by its UID.
func (s *StorageClass) findByUID(ctx context.Context, uid string) (*storagev1.StorageClass, error) {
	list, err := s.Client.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
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
