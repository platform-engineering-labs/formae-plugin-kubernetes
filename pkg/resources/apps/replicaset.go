// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package apps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
)

const ResourceTypeReplicaSet = "K8S::Apps::ReplicaSet"

func init() {
	registry.Register(
		ResourceTypeReplicaSet,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &ReplicaSet{Client: client, Config: cfg}
		},
	)
}

// ReplicaSet implements the provisioner for K8S::ReplicaSet resources.
type ReplicaSet struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &ReplicaSet{}

func (r *ReplicaSet) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var rs *appsv1ac.ReplicaSetApplyConfiguration
	if err := json.Unmarshal(request.Properties, &rs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal replicaset properties: %w", err)
	}

	namespace := r.Config.EffectiveNamespace()
	if rs.Namespace != nil {
		namespace = *rs.Namespace
	}

	result, err := r.Client.AppsV1().ReplicaSets(namespace).Apply(ctx, rs, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply replicaset: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.ReplicaSetApplyConfiguration](result, "ReplicaSet", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get replicaset live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    r.fromConditions(result.Status.Conditions),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      r.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (r *ReplicaSet) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := r.Client.AppsV1().ReplicaSets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get replicaset: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.ReplicaSetApplyConfiguration](result, "ReplicaSet", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get replicaset live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (r *ReplicaSet) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var rs *appsv1ac.ReplicaSetApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &rs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal replicaset properties: %w", err)
	}

	namespace := r.Config.EffectiveNamespace()
	if rs.Namespace != nil {
		namespace = *rs.Namespace
	}

	result, err := r.Client.AppsV1().ReplicaSets(namespace).Apply(ctx, rs, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply replicaset: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, rs, func(name string, patch []byte) error {
		_, err := r.Client.AppsV1().ReplicaSets(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile replicaset metadata: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.ReplicaSetApplyConfiguration](result, "ReplicaSet", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get replicaset live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    r.fromConditions(result.Status.Conditions),
			RequestID:          result.ResourceVersion,
			StatusMessage:      r.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (r *ReplicaSet) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := r.Client.AppsV1().ReplicaSets(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete replicaset: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (r *ReplicaSet) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := r.Client.AppsV1().ReplicaSets(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get replicaset status: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.ReplicaSetApplyConfiguration](result, "ReplicaSet", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get replicaset live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    r.fromConditions(result.Status.Conditions),
			RequestID:          request.RequestID,
			StatusMessage:      r.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (r *ReplicaSet) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := r.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := r.Client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list replicasets: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, rs := range result.Items {
		// Skip ReplicaSets owned by a controller (typically a Deployment).
		// Filtering at List level prevents formae from processing controller-
		// created ReplicaSets through the changeset pipeline during discovery.
		if len(rs.OwnerReferences) > 0 {
			continue
		}
		nativeIDs = append(nativeIDs, prov.NativeID(rs.Namespace, rs.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// fromConditions maps K8S ReplicaSet conditions to Formae OperationStatus.
func (r *ReplicaSet) fromConditions(conditions []appsv1.ReplicaSetCondition) resource.OperationStatus {
	for _, cond := range conditions {
		if cond.Type == appsv1.ReplicaSetReplicaFailure && cond.Status == "True" {
			return resource.OperationStatusFailure
		}
	}
	return resource.OperationStatusSuccess
}

// statusMessage builds a status message from ReplicaSet status.
func (r *ReplicaSet) statusMessage(status appsv1.ReplicaSetStatus) string {
	return fmt.Sprintf("replicas: %d/%d ready, %d available",
		status.ReadyReplicas, status.Replicas, status.AvailableReplicas)
}
