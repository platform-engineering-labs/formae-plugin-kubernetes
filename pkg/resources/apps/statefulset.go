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

	namespace, err := prov.ResolveCreateNamespace(sts.Namespace, ResourceTypeStatefulSet)
	if err != nil {
		return nil, err
	}

	result, err := ss.Client.AppsV1().StatefulSets(namespace).Apply(ctx, sts, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
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
			OperationStatus:    ss.operationStatus(result),
			RequestID:          result.ResourceVersion,
			StatusMessage:      ss.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ss *StatefulSet) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	namespace, err := prov.ResolveCreateNamespace(sts.Namespace, ResourceTypeStatefulSet)
	if err != nil {
		return nil, err
	}

	result, err := ss.Client.AppsV1().StatefulSets(namespace).Apply(ctx, sts, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply statefulset: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, sts, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := ss.Client.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.MergePatchType, patch, opts)
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
			OperationStatus:    ss.operationStatus(result),
			RequestID:          result.ResourceVersion,
			StatusMessage:      ss.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ss *StatefulSet) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	// Foreground propagation: cascade to managed Pods (and headless Service)
	// before the StatefulSet object itself disappears. Note: PVCs created from
	// volumeClaimTemplates are intentionally retained by K8s — users must
	// delete those separately or rely on whenDeleted retention policies.
	propagation := metav1.DeletePropagationForeground
	err = ss.Client.AppsV1().StatefulSets(ns).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
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
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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
			OperationStatus:    ss.operationStatus(result),
			RequestID:          request.RequestID,
			StatusMessage:      ss.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ss *StatefulSet) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeStatefulSet)
	if err != nil {
		return nil, err
	}

	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := ss.Client.AppsV1().StatefulSets(namespace).List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, sts := range page.Items {
			nativeIDs = append(nativeIDs, prov.NativeID(sts.Namespace, sts.Name))
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list statefulsets: %w", err)
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

// operationStatus maps StatefulSet state to Formae OperationStatus.
//
// We gate on the StatefulSet controller having observed the latest spec
// (status.observedGeneration vs metadata.generation) first — otherwise both
// status.Replicas and status.ReadyReplicas default to 0 immediately after
// Apply and the naive "ReadyReplicas < Replicas" check would incorrectly
// return Success for a brand-new StatefulSet.
//
// Once the controller has reconciled, we compare against the *desired*
// replica count (spec.Replicas, defaulting to 1) for both ready and
// updated replicas to catch in-flight rolling updates.
func (ss *StatefulSet) operationStatus(sts *appsv1.StatefulSet) resource.OperationStatus {
	if !prov.ObservedGenerationReady(&sts.ObjectMeta, sts.Status.ObservedGeneration) {
		return resource.OperationStatusInProgress
	}
	var desired int32 = 1
	if sts.Spec.Replicas != nil {
		desired = *sts.Spec.Replicas
	}
	if sts.Status.ReadyReplicas < desired || sts.Status.UpdatedReplicas < desired {
		return resource.OperationStatusInProgress
	}
	return resource.OperationStatusSuccess
}

// statusMessage builds a status message from StatefulSet status.
func (ss *StatefulSet) statusMessage(status appsv1.StatefulSetStatus) string {
	return fmt.Sprintf("replicas: %d/%d ready, %d updated",
		status.ReadyReplicas, status.Replicas, status.UpdatedReplicas)
}
