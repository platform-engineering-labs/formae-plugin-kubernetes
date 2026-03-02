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

const ResourceTypeCSIDriver = "K8S::Storage::CSIDriver"

func init() {
	registry.Register(
		ResourceTypeCSIDriver,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &CSIDriver{Client: client, Config: cfg}
		},
	)
}

// CSIDriver implements the provisioner for K8S::Storage::CSIDriver resources.
type CSIDriver struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &CSIDriver{}

func (c *CSIDriver) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var cd *storagev1ac.CSIDriverApplyConfiguration
	if err := json.Unmarshal(request.Properties, &cd); err != nil {
		return nil, fmt.Errorf("failed to unmarshal csidriver properties: %w", err)
	}

	result, err := c.Client.StorageV1().CSIDrivers().Apply(ctx, cd, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply csidriver: %w", err)
	}

	ext, err := storagev1ac.ExtractCSIDriver(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract csidriver: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal csidriver properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (c *CSIDriver) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := c.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get csidriver: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := storagev1ac.ExtractCSIDriver(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract csidriver: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal csidriver properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (c *CSIDriver) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var cd *storagev1ac.CSIDriverApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &cd); err != nil {
		return nil, fmt.Errorf("failed to unmarshal csidriver properties: %w", err)
	}

	result, err := c.Client.StorageV1().CSIDrivers().Apply(ctx, cd, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply csidriver: %w", err)
	}

	ext, err := storagev1ac.ExtractCSIDriver(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract csidriver: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal csidriver properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (c *CSIDriver) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	cd, err := c.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find csidriver: %w", err)
	}
	if cd == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = c.Client.StorageV1().CSIDrivers().Delete(ctx, cd.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete csidriver: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (c *CSIDriver) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := c.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get csidriver status: %w", err)
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

	ext, err := storagev1ac.ExtractCSIDriver(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract csidriver: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal csidriver properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (c *CSIDriver) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := c.Client.StorageV1().CSIDrivers().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list csidrivers: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, cd := range result.Items {
		nativeIDs = append(nativeIDs, string(cd.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a CSIDriver by its UID.
func (c *CSIDriver) findByUID(ctx context.Context, uid string) (*storagev1.CSIDriver, error) {
	list, err := c.Client.StorageV1().CSIDrivers().List(ctx, metav1.ListOptions{})
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
