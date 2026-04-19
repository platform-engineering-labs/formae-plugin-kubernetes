// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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

// terminatingGracePeriod is how long we let a namespace stay in Terminating
// phase before force-removing the `kubernetes` finalizer. Pods stuck on a
// NotReady node (common on EKS AutoMode teardown) prevent K8s's own namespace
// controller from completing deletion; without escalation the namespace hangs
// forever and the formae destroy cascade-fails every downstream resource.
const terminatingGracePeriod = 2 * time.Minute

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
		FieldManager: prov.FieldManager,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply namespace: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.NamespaceApplyConfiguration](result, "Namespace", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to extract namespace state: %w", err)
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

	properties, err := prov.LiveState[v1coreac.NamespaceApplyConfiguration](result, "Namespace", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to extract namespace state: %w", err)
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
		FieldManager: prov.FieldManager,
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

	// Re-read after ReconcileMetadata to get the true post-patch state.
	result, err = n.Client.CoreV1().Namespaces().Get(ctx, result.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to re-read namespace after reconcile: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.NamespaceApplyConfiguration](result, "Namespace", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to extract namespace state: %w", err)
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

	// Escalate a stuck Terminating namespace by clearing spec.finalizers.
	// K8s's namespace controller reports ContentDeletionFailed when pods can't
	// terminate (common on EKS AutoMode when the underlying node goes NotReady
	// mid-teardown). Waiting forever cascades failures to downstream AWS
	// resources.
	if result.Status.Phase == v1.NamespaceTerminating && isContentDeletionFailed(result) && n.terminatingLongEnough(result) {
		if err := n.forceRemoveFinalizers(ctx, result); err != nil {
			// Log context but return InProgress so Formae keeps polling.
			return &resource.StatusResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationCheckStatus,
					OperationStatus: resource.OperationStatusInProgress,
					RequestID:       request.RequestID,
					NativeID:        result.Name,
					StatusMessage:   fmt.Sprintf("namespace stuck terminating; finalizer-clear attempt failed: %v", err),
				},
			}, nil
		}
		// Next Status poll will see NotFound and report success.
		return &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCheckStatus,
				OperationStatus: resource.OperationStatusInProgress,
				RequestID:       request.RequestID,
				NativeID:        result.Name,
				StatusMessage:   "cleared stuck finalizer; namespace deletion should complete",
			},
		}, nil
	}

	properties, err := prov.LiveState[v1coreac.NamespaceApplyConfiguration](result, "Namespace", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to extract namespace state: %w", err)
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

// isContentDeletionFailed reports whether K8s has given up on reclaiming
// namespace content — typically pods left behind when a kubelet stops ack-ing
// terminations.
func isContentDeletionFailed(ns *v1.Namespace) bool {
	for _, c := range ns.Status.Conditions {
		if c.Type == v1.NamespaceDeletionContentFailure && c.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// terminatingLongEnough reports whether the namespace has been in Terminating
// state long enough to warrant escalation. Uses deletionTimestamp as the
// clock, so a freshly-deleted-but-slow namespace won't trigger.
func (n *Namespace) terminatingLongEnough(ns *v1.Namespace) bool {
	if ns.DeletionTimestamp == nil {
		return false
	}
	return time.Since(ns.DeletionTimestamp.Time) >= terminatingGracePeriod
}

// forceRemoveFinalizers empties spec.finalizers via the /finalize subresource,
// equivalent to `kubectl replace --raw /api/v1/namespaces/<name>/finalize`.
// The `kubernetes` finalizer is owned by K8s itself; removing it unblocks
// namespace deletion when the controller can't make progress.
func (n *Namespace) forceRemoveFinalizers(ctx context.Context, ns *v1.Namespace) error {
	if len(ns.Spec.Finalizers) == 0 {
		return nil
	}
	patched := ns.DeepCopy()
	patched.Spec.Finalizers = nil
	_, err := n.Client.CoreV1().Namespaces().Finalize(ctx, patched, metav1.UpdateOptions{})
	return err
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

