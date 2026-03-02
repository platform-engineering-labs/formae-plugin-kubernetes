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
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
)

const ResourceTypeStatefulSet = "K8S::Apps::StatefulSet"

func init() {
	registry.Register(
		ResourceTypeStatefulSet,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &StatefulSet{Client: client, Config: cfg}
		},
	)
}

// StatefulSet implements the provisioner for K8S::Apps::StatefulSet resources.
type StatefulSet struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &StatefulSet{}

func (ss *StatefulSet) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var sts *appsv1ac.StatefulSetApplyConfiguration
	if err := json.Unmarshal(request.Properties, &sts); err != nil {
		return nil, fmt.Errorf("failed to unmarshal statefulset properties: %w", err)
	}

	namespace := ss.Config.EffectiveNamespace()
	if sts.Namespace != nil {
		namespace = *sts.Namespace
	}

	result, err := ss.Client.AppsV1().StatefulSets(namespace).Apply(ctx, sts, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply statefulset: %w", err)
	}

	ext, err := appsv1ac.ExtractStatefulSet(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract statefulset: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal statefulset properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    ss.operationStatus(result.Status),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      ss.statusMessage(result.Status),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (ss *StatefulSet) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := ss.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get statefulset: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := appsv1ac.ExtractStatefulSet(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract statefulset: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal statefulset properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (ss *StatefulSet) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var sts *appsv1ac.StatefulSetApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &sts); err != nil {
		return nil, fmt.Errorf("failed to unmarshal statefulset properties: %w", err)
	}

	namespace := ss.Config.EffectiveNamespace()
	if sts.Namespace != nil {
		namespace = *sts.Namespace
	}

	result, err := ss.Client.AppsV1().StatefulSets(namespace).Apply(ctx, sts, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply statefulset: %w", err)
	}

	ext, err := appsv1ac.ExtractStatefulSet(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract statefulset: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal statefulset properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    ss.operationStatus(result.Status),
			RequestID:          result.ResourceVersion,
			StatusMessage:      ss.statusMessage(result.Status),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (ss *StatefulSet) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	sts, err := ss.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find statefulset: %w", err)
	}
	if sts == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = ss.Client.AppsV1().StatefulSets(sts.Namespace).Delete(ctx, sts.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete statefulset: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (ss *StatefulSet) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := ss.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get statefulset status: %w", err)
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

	ext, err := appsv1ac.ExtractStatefulSet(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract statefulset: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal statefulset properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    ss.operationStatus(result.Status),
			RequestID:          request.RequestID,
			StatusMessage:      ss.statusMessage(result.Status),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (ss *StatefulSet) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := ss.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := ss.Client.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list statefulsets: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, sts := range result.Items {
		nativeIDs = append(nativeIDs, string(sts.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a statefulset by its UID across all namespaces.
func (ss *StatefulSet) findByUID(ctx context.Context, uid string) (*appsv1.StatefulSet, error) {
	list, err := ss.Client.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
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

// operationStatus maps StatefulSet status to Formae OperationStatus.
// InProgress when ReadyReplicas < Replicas.
func (ss *StatefulSet) operationStatus(status appsv1.StatefulSetStatus) resource.OperationStatus {
	if status.ReadyReplicas < status.Replicas {
		return resource.OperationStatusInProgress
	}
	return resource.OperationStatusSuccess
}

// statusMessage builds a status message from StatefulSet status.
func (ss *StatefulSet) statusMessage(status appsv1.StatefulSetStatus) string {
	return fmt.Sprintf("replicas: %d/%d ready, %d updated",
		status.ReadyReplicas, status.Replicas, status.UpdatedReplicas)
}
