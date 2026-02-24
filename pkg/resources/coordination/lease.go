// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coordinationv1ac "k8s.io/client-go/applyconfigurations/coordination/v1"
)

const ResourceTypeLease = "K8S::Coordination::Lease"

func init() {
	registry.Register(
		ResourceTypeLease,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Lease{Client: client, Config: cfg}
		},
	)
}

// Lease implements the provisioner for K8S::Coordination::Lease resources.
type Lease struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Lease{}

func (l *Lease) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var lease *coordinationv1ac.LeaseApplyConfiguration
	if err := json.Unmarshal(request.Properties, &lease); err != nil {
		return nil, fmt.Errorf("failed to unmarshal lease properties: %w", err)
	}

	namespace := l.Config.EffectiveNamespace()
	if lease.Namespace != nil {
		namespace = *lease.Namespace
	}

	result, err := l.Client.CoordinationV1().Leases(namespace).Apply(ctx, lease, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply lease: %w", err)
	}

	ext, err := coordinationv1ac.ExtractLease(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract lease: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal lease properties: %w", err)
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

func (l *Lease) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := l.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get lease: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := coordinationv1ac.ExtractLease(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract lease: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal lease properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (l *Lease) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var lease *coordinationv1ac.LeaseApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &lease); err != nil {
		return nil, fmt.Errorf("failed to unmarshal lease properties: %w", err)
	}

	namespace := l.Config.EffectiveNamespace()
	if lease.Namespace != nil {
		namespace = *lease.Namespace
	}

	result, err := l.Client.CoordinationV1().Leases(namespace).Apply(ctx, lease, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply lease: %w", err)
	}

	ext, err := coordinationv1ac.ExtractLease(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract lease: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal lease properties: %w", err)
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

func (l *Lease) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	lease, err := l.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find lease: %w", err)
	}
	if lease == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = l.Client.CoordinationV1().Leases(lease.Namespace).Delete(ctx, lease.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete lease: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (l *Lease) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := l.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get lease status: %w", err)
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

	ext, err := coordinationv1ac.ExtractLease(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract lease: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal lease properties: %w", err)
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

func (l *Lease) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := l.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := l.Client.CoordinationV1().Leases(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list leases: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, lease := range result.Items {
		nativeIDs = append(nativeIDs, string(lease.ObjectMeta.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a Lease by its UID across all namespaces.
func (l *Lease) findByUID(ctx context.Context, uid string) (*coordinationv1.Lease, error) {
	list, err := l.Client.CoordinationV1().Leases(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
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
