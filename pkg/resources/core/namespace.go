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

const ResourceTypeNamespace = "K8S::Core::Namespace"

func init() {
	registry.Register(
		ResourceTypeNamespace,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Namespace{Client: client, Config: cfg}
		},
	)
}

// Namespace implements the provisioner for K8S::Namespace resources.
type Namespace struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Namespace{}

func (n *Namespace) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var ns *v1coreac.NamespaceApplyConfiguration
	if err := json.Unmarshal(request.Properties, &ns); err != nil {
		return nil, fmt.Errorf("failed to unmarshal namespace properties: %w", err)
	}

	result, err := n.Client.CoreV1().Namespaces().Apply(ctx, ns, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply namespace: %w", err)
	}

	// Extract only the fields managed by formae
	ext, err := v1coreac.ExtractNamespace(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract namespace: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal namespace properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    n.fromPhase(result.Status.Phase),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (n *Namespace) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	// K8S API needs name, but we have UID. Use field selector to find by UID.
	result, err := n.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get namespace: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	// Extract only the fields managed by formae
	ext, err := v1coreac.ExtractNamespace(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract namespace: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal namespace properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (n *Namespace) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var ns *v1coreac.NamespaceApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &ns); err != nil {
		return nil, fmt.Errorf("failed to unmarshal namespace properties: %w", err)
	}

	result, err := n.Client.CoreV1().Namespaces().Apply(ctx, ns, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply namespace: %w", err)
	}

	// Extract only the fields managed by formae
	ext, err := v1coreac.ExtractNamespace(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract namespace: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal namespace properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    n.fromPhase(result.Status.Phase),
			RequestID:          result.ResourceVersion,
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (n *Namespace) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	// Find namespace by UID first to get the name
	ns, err := n.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			// Already deleted, consider success
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find namespace: %w", err)
	}
	if ns == nil {
		// Already deleted
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = n.Client.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete namespace: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (n *Namespace) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := n.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get namespace status: %w", err)
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
	ext, err := v1coreac.ExtractNamespace(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract namespace: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal namespace properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    n.fromPhase(result.Status.Phase),
			RequestID:          request.RequestID,
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (n *Namespace) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	result, err := n.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, ns := range result.Items {
		nativeIDs = append(nativeIDs, string(ns.ObjectMeta.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a namespace by its UID.
// K8S doesn't support metadata.uid field selector for Namespaces,
// so we list all and filter in memory.
// Returns nil if not found (no error).
func (n *Namespace) findByUID(ctx context.Context, uid string) (*v1.Namespace, error) {
	list, err := n.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
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

// fromPhase maps K8S NamespacePhase to Formae OperationStatus.
func (n *Namespace) fromPhase(phase v1.NamespacePhase) resource.OperationStatus {
	switch phase {
	case v1.NamespaceActive:
		return resource.OperationStatusSuccess
	case v1.NamespaceTerminating:
		return resource.OperationStatusInProgress
	default:
		return resource.OperationStatusSuccess
	}
}
