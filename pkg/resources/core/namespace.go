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
			NativeID:           result.Name,
			ResourceProperties: properties,
		},
	}, nil
}

func (n *Namespace) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := n.Client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get namespace: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.NamespaceApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace live state: %w", err)
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
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply namespace: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, ns, func(name string, patch []byte) error {
		_, err := n.Client.CoreV1().Namespaces().Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile namespace metadata: %w", err)
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
			NativeID:           result.Name,
			ResourceProperties: properties,
		},
	}, nil
}

func (n *Namespace) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	_, name := prov.ParseNativeID(request.NativeID)
	err := n.Client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
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
	_, name := prov.ParseNativeID(request.NativeID)
	result, err := n.Client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
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

	properties, err := prov.LiveState[v1coreac.NamespaceApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    n.fromPhase(result.Status.Phase),
			RequestID:          request.RequestID,
			NativeID:           result.Name,
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
		nativeIDs = append(nativeIDs, ns.Name)
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
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
