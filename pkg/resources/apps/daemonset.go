// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package apps

import (
	"context"
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
	if err := prov.UnmarshalApplyConfig(request.Properties, &daemonset); err != nil {
		return nil, fmt.Errorf("failed to unmarshal daemonset properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(daemonset.Namespace, ResourceTypeDaemonSet)
	if err != nil {
		return nil, err
	}

	result, err := ds.Client.AppsV1().DaemonSets(namespace).Apply(ctx, daemonset, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply daemonset: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.DaemonSetApplyConfiguration](result, "DaemonSet", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get daemonset live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    ds.operationStatus(result),
			RequestID:          result.ResourceVersion,
			StatusMessage:      ds.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ds *DaemonSet) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := ds.Client.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get daemonset: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.DaemonSetApplyConfiguration](result, "DaemonSet", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get daemonset live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (ds *DaemonSet) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var daemonset *appsv1ac.DaemonSetApplyConfiguration
	if err := prov.UnmarshalApplyConfig(request.DesiredProperties, &daemonset); err != nil {
		return nil, fmt.Errorf("failed to unmarshal daemonset properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(daemonset.Namespace, ResourceTypeDaemonSet)
	if err != nil {
		return nil, err
	}

	result, err := ds.Client.AppsV1().DaemonSets(namespace).Apply(ctx, daemonset, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply daemonset: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, daemonset, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := ds.Client.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile daemonset metadata: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.DaemonSetApplyConfiguration](result, "DaemonSet", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get daemonset live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    ds.operationStatus(result),
			RequestID:          result.ResourceVersion,
			StatusMessage:      ds.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ds *DaemonSet) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	// Foreground propagation: cascade to managed Pods before the DaemonSet
	// object disappears, so destroy completes only once children are gone.
	propagation := metav1.DeletePropagationForeground
	err = ds.Client.AppsV1().DaemonSets(ns).Delete(ctx, name, metav1.DeleteOptions{
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
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := ds.Client.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
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

	properties, err := prov.LiveState[appsv1ac.DaemonSetApplyConfiguration](result, "DaemonSet", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get daemonset live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    ds.operationStatus(result),
			RequestID:          request.RequestID,
			StatusMessage:      ds.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (ds *DaemonSet) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeDaemonSet)
	if err != nil {
		return nil, err
	}

	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := ds.Client.AppsV1().DaemonSets(namespace).List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, daemonset := range page.Items {
			nativeIDs = append(nativeIDs, prov.NativeID(daemonset.Namespace, daemonset.Name))
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list daemonsets: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// operationStatus maps DaemonSet state to Formae OperationStatus.
//
// We gate on the DaemonSet controller having observed the latest spec first
// (status.observedGeneration vs metadata.generation). Immediately after Apply
// both DesiredNumberScheduled and NumberReady default to 0 and the naive
// comparison would incorrectly report Success before the controller has had
// a chance to schedule pods.
//
// Once observed, we treat the rollout as InProgress while pods are still
// being updated or are not yet ready.
func (ds *DaemonSet) operationStatus(daemonset *appsv1.DaemonSet) resource.OperationStatus {
	if !prov.ObservedGenerationReady(&daemonset.ObjectMeta, daemonset.Status.ObservedGeneration) {
		return resource.OperationStatusInProgress
	}
	if daemonset.Status.UpdatedNumberScheduled < daemonset.Status.DesiredNumberScheduled ||
		daemonset.Status.NumberReady < daemonset.Status.DesiredNumberScheduled {
		return resource.OperationStatusInProgress
	}
	return resource.OperationStatusSuccess
}

// statusMessage builds a status message from DaemonSet status.
func (ds *DaemonSet) statusMessage(status appsv1.DaemonSetStatus) string {
	return fmt.Sprintf("desired: %d, ready: %d, updated: %d, available: %d",
		status.DesiredNumberScheduled, status.NumberReady, status.UpdatedNumberScheduled, status.NumberAvailable)
}
