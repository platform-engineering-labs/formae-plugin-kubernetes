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
		FieldManager: prov.FieldManager,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply statefulset: %w", err)
	}

	properties, err := extractStatefulSetState(result)
	if err != nil {
		return nil, fmt.Errorf("failed to get statefulset live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    ss.operationStatus(result.Status),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      ss.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ss *StatefulSet) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := ss.Client.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get statefulset: %w", err)
	}

	properties, err := extractStatefulSetState(result)
	if err != nil {
		return nil, fmt.Errorf("failed to get statefulset live state: %w", err)
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
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply statefulset: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, sts, func(name string, patch []byte) error {
		_, err := ss.Client.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile statefulset metadata: %w", err)
	}

	properties, err := extractStatefulSetState(result)
	if err != nil {
		return nil, fmt.Errorf("failed to get statefulset live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    ss.operationStatus(result.Status),
			RequestID:          result.ResourceVersion,
			StatusMessage:      ss.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ss *StatefulSet) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := ss.Client.AppsV1().StatefulSets(ns).Delete(ctx, name, metav1.DeleteOptions{})
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
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := ss.Client.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
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

	properties, err := extractStatefulSetState(result)
	if err != nil {
		return nil, fmt.Errorf("failed to get statefulset live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    ss.operationStatus(result.Status),
			RequestID:          request.RequestID,
			StatusMessage:      ss.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
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
		nativeIDs = append(nativeIDs, prov.NativeID(sts.Namespace, sts.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// extractStatefulSetState extracts SSA state and strips server-managed fields
// from volumeClaimTemplates (status, which the API server injects into VCTs).
func extractStatefulSetState(result *appsv1.StatefulSet) (json.RawMessage, error) {
	properties, err := prov.LiveState[appsv1ac.StatefulSetApplyConfiguration](result, "StatefulSet", "apps/v1")
	if err != nil {
		return nil, err
	}
	return stripVCTStatus(properties)
}

// stripVCTStatus removes "status" from each volumeClaimTemplates entry.
// The K8S API server injects status into VCTs but it's a server-managed field
// that shouldn't appear in the desired-state properties.
func stripVCTStatus(data json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return data, nil //nolint:nilerr // not a JSON object, return as-is
	}
	specRaw, ok := obj["spec"]
	if !ok {
		return data, nil
	}
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return data, nil //nolint:nilerr
	}
	vctRaw, ok := spec["volumeClaimTemplates"]
	if !ok {
		return data, nil
	}
	var vcts []map[string]json.RawMessage
	if err := json.Unmarshal(vctRaw, &vcts); err != nil {
		return data, nil //nolint:nilerr
	}
	for i := range vcts {
		delete(vcts[i], "status")
	}
	vctBytes, err := json.Marshal(vcts)
	if err != nil {
		return data, nil //nolint:nilerr
	}
	spec["volumeClaimTemplates"] = vctBytes
	specBytes, err := json.Marshal(spec)
	if err != nil {
		return data, nil //nolint:nilerr
	}
	obj["spec"] = specBytes
	return json.Marshal(obj)
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
