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

const ResourceTypeDeployment = "K8S::Apps::Deployment"

func init() {
	registry.Register(
		ResourceTypeDeployment,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Deployment{Client: client, Config: cfg}
		},
	)
}

// Deployment implements the provisioner for K8S::Apps::Deployment resources.
type Deployment struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Deployment{}

func (d *Deployment) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var deploy *appsv1ac.DeploymentApplyConfiguration
	if err := json.Unmarshal(request.Properties, &deploy); err != nil {
		return nil, fmt.Errorf("failed to unmarshal deployment properties: %w", err)
	}

	namespace := "default"
	if deploy.Namespace != nil {
		namespace = *deploy.Namespace
	}

	result, err := d.Client.AppsV1().Deployments(namespace).Apply(ctx, deploy, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply deployment: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.DeploymentApplyConfiguration](result, "Deployment", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    d.fromConditions(result.Status.Conditions),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      d.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (d *Deployment) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := d.Client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get deployment: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.DeploymentApplyConfiguration](result, "Deployment", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (d *Deployment) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var deploy *appsv1ac.DeploymentApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &deploy); err != nil {
		return nil, fmt.Errorf("failed to unmarshal deployment properties: %w", err)
	}

	namespace := "default"
	if deploy.Namespace != nil {
		namespace = *deploy.Namespace
	}

	result, err := d.Client.AppsV1().Deployments(namespace).Apply(ctx, deploy, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply deployment: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, deploy, func(name string, patch []byte) error {
		_, err := d.Client.AppsV1().Deployments(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile deployment metadata: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.DeploymentApplyConfiguration](result, "Deployment", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    d.fromConditions(result.Status.Conditions),
			RequestID:          result.ResourceVersion,
			StatusMessage:      d.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (d *Deployment) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := d.Client.AppsV1().Deployments(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete deployment: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (d *Deployment) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := d.Client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get deployment status: %w", err)
	}

	properties, err := prov.LiveState[appsv1ac.DeploymentApplyConfiguration](result, "Deployment", "apps/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    d.fromConditions(result.Status.Conditions),
			RequestID:          request.RequestID,
			StatusMessage:      d.statusMessage(result.Status),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (d *Deployment) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := "default"
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := d.Client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, deploy := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(deploy.Namespace, deploy.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// fromConditions maps K8S Deployment conditions to Formae OperationStatus.
// Reports Failure when K8S itself declares the rollout stuck or a replica
// cannot be created. Runtime pod health (e.g. CrashLoopBackOff) is a
// separate concern from provisioning — those are surfaced via statusMessage.
func (d *Deployment) fromConditions(conditions []appsv1.DeploymentCondition) resource.OperationStatus {
	for _, cond := range conditions {
		if cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == "True" {
			return resource.OperationStatusFailure
		}
		if cond.Type == appsv1.DeploymentProgressing && cond.Status == "False" && cond.Reason == "ProgressDeadlineExceeded" {
			return resource.OperationStatusFailure
		}
	}
	return resource.OperationStatusSuccess
}

// statusMessage builds a status message from Deployment status.
func (d *Deployment) statusMessage(status appsv1.DeploymentStatus) string {
	return fmt.Sprintf("replicas: %d/%d ready, %d available, %d updated",
		status.ReadyReplicas, status.Replicas, status.AvailableReplicas, status.UpdatedReplicas)
}
