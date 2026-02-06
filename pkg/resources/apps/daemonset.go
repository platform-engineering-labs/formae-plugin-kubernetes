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

const ResourceTypeDaemonSet = "K8S::Apps::DaemonSet"

func init() {
	registry.Register(
		ResourceTypeDaemonSet,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &DaemonSet{Client: client, Config: cfg}
		},
	)
}

// DaemonSet implements the provisioner for K8S::Apps::DaemonSet resources.
type DaemonSet struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &DaemonSet{}

func (ds *DaemonSet) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var daemonset *appsv1ac.DaemonSetApplyConfiguration
	if err := json.Unmarshal(request.Properties, &daemonset); err != nil {
		return nil, fmt.Errorf("failed to unmarshal daemonset properties: %w", err)
	}

	namespace := ds.Config.EffectiveNamespace()
	if daemonset.Namespace != nil {
		namespace = *daemonset.Namespace
	}

	result, err := ds.Client.AppsV1().DaemonSets(namespace).Apply(ctx, daemonset, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply daemonset: %w", err)
	}

	ext, err := appsv1ac.ExtractDaemonSet(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract daemonset: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal daemonset properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    ds.operationStatus(result.Status),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      ds.statusMessage(result.Status),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (ds *DaemonSet) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := ds.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get daemonset: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := appsv1ac.ExtractDaemonSet(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract daemonset: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal daemonset properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (ds *DaemonSet) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var daemonset *appsv1ac.DaemonSetApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &daemonset); err != nil {
		return nil, fmt.Errorf("failed to unmarshal daemonset properties: %w", err)
	}

	namespace := ds.Config.EffectiveNamespace()
	if daemonset.Namespace != nil {
		namespace = *daemonset.Namespace
	}

	result, err := ds.Client.AppsV1().DaemonSets(namespace).Apply(ctx, daemonset, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply daemonset: %w", err)
	}

	ext, err := appsv1ac.ExtractDaemonSet(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract daemonset: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal daemonset properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    ds.operationStatus(result.Status),
			RequestID:          result.ResourceVersion,
			StatusMessage:      ds.statusMessage(result.Status),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (ds *DaemonSet) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	daemonset, err := ds.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find daemonset: %w", err)
	}
	if daemonset == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = ds.Client.AppsV1().DaemonSets(daemonset.Namespace).Delete(ctx, daemonset.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete daemonset: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (ds *DaemonSet) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := ds.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get daemonset status: %w", err)
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

	ext, err := appsv1ac.ExtractDaemonSet(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract daemonset: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal daemonset properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    ds.operationStatus(result.Status),
			RequestID:          request.RequestID,
			StatusMessage:      ds.statusMessage(result.Status),
			NativeID:           string(result.ObjectMeta.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (ds *DaemonSet) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := ds.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := ds.Client.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list daemonsets: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, daemonset := range result.Items {
		nativeIDs = append(nativeIDs, string(daemonset.ObjectMeta.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a daemonset by its UID across all namespaces.
func (ds *DaemonSet) findByUID(ctx context.Context, uid string) (*appsv1.DaemonSet, error) {
	list, err := ds.Client.AppsV1().DaemonSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
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

// operationStatus maps DaemonSet status to Formae OperationStatus.
// InProgress when NumberReady < DesiredNumberScheduled.
func (ds *DaemonSet) operationStatus(status appsv1.DaemonSetStatus) resource.OperationStatus {
	if status.NumberReady < status.DesiredNumberScheduled {
		return resource.OperationStatusInProgress
	}
	return resource.OperationStatusSuccess
}

// statusMessage builds a status message from DaemonSet status.
func (ds *DaemonSet) statusMessage(status appsv1.DaemonSetStatus) string {
	return fmt.Sprintf("desired: %d, ready: %d, updated: %d, available: %d",
		status.DesiredNumberScheduled, status.NumberReady, status.UpdatedNumberScheduled, status.NumberAvailable)
}
