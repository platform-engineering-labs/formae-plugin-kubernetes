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

	// Re-read to get the full object so LiveState matches what Read returns.
	current, err := n.Client.CoreV1().Namespaces().Get(ctx, result.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace after create: %w", err)
	}

	properties, err := namespaceLiveState(current)
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace live state: %w", err)
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

	properties, err := namespaceLiveState(result)
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

	// Re-read to get the full object so LiveState matches what Read returns.
	current, err := n.Client.CoreV1().Namespaces().Get(ctx, result.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace after update: %w", err)
	}

	properties, err := namespaceLiveState(current)
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    n.fromPhase(current.Status.Phase),
			RequestID:          current.ResourceVersion,
			NativeID:           current.Name,
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

	properties, err := namespaceLiveState(result)
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

// namespaceLiveState returns the live state of a Namespace with only
// user-controllable metadata (name, namespace, labels, annotations).
// All server-managed fields (uid, resourceVersion, creationTimestamp,
// spec, status) are stripped to prevent property oscillation between
// operations. The K8S-managed label "kubernetes.io/metadata.name" is
// also removed since it's added automatically by the API server.
func namespaceLiveState(apiObject any) ([]byte, error) {
	raw, err := json.Marshal(apiObject)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal api object: %w", err)
	}

	var full map[string]interface{}
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, fmt.Errorf("failed to unmarshal namespace: %w", err)
	}

	result := map[string]interface{}{}
	if meta, ok := full["metadata"].(map[string]interface{}); ok {
		clean := map[string]interface{}{}
		for _, key := range []string{"name", "namespace", "labels", "annotations"} {
			if v, exists := meta[key]; exists {
				clean[key] = v
			}
		}
		// Remove K8S-managed label added by NamespaceDefaultLabelName admission plugin.
		if labels, ok := clean["labels"].(map[string]interface{}); ok {
			delete(labels, "kubernetes.io/metadata.name")
		}
		result["metadata"] = clean
	}

	return json.Marshal(result)
}
