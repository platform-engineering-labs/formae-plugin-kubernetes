// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package batch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	batchv1ac "k8s.io/client-go/applyconfigurations/batch/v1"
)

const ResourceTypeCronJob = "K8S::Batch::CronJob"

func init() {
	registry.Register(
		ResourceTypeCronJob,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &CronJob{Client: client, Config: cfg}
		},
	)
}

// CronJob implements the provisioner for K8S::Batch::CronJob resources.
type CronJob struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &CronJob{}

func (cj *CronJob) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var cronjob *batchv1ac.CronJobApplyConfiguration
	if err := json.Unmarshal(request.Properties, &cronjob); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cronjob properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(cronjob.Namespace, ResourceTypeCronJob)
	if err != nil {
		return nil, err
	}

	result, err := cj.Client.BatchV1().CronJobs(namespace).Apply(ctx, cronjob, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply cronjob: %w", err)
	}

	properties, err := prov.LiveState[batchv1ac.CronJobApplyConfiguration](result, "CronJob", "batch/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get cronjob live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (cj *CronJob) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := cj.Client.BatchV1().CronJobs(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get cronjob: %w", err)
	}

	properties, err := prov.LiveState[batchv1ac.CronJobApplyConfiguration](result, "CronJob", "batch/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get cronjob live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (cj *CronJob) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var cronjob *batchv1ac.CronJobApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &cronjob); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cronjob properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(cronjob.Namespace, ResourceTypeCronJob)
	if err != nil {
		return nil, err
	}

	result, err := cj.Client.BatchV1().CronJobs(namespace).Apply(ctx, cronjob, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply cronjob: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, cronjob, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := cj.Client.BatchV1().CronJobs(namespace).Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile cronjob metadata: %w", err)
	}

	properties, err := prov.LiveState[batchv1ac.CronJobApplyConfiguration](result, "CronJob", "batch/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get cronjob live state: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (cj *CronJob) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}

	err = cj.Client.BatchV1().CronJobs(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete cronjob: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (cj *CronJob) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	result, err := cj.Client.BatchV1().CronJobs(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get cronjob status: %w", err)
	}

	properties, err := prov.LiveState[batchv1ac.CronJobApplyConfiguration](result, "CronJob", "batch/v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get cronjob live state: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (cj *CronJob) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeCronJob)
	if err != nil {
		return nil, err
	}

	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := cj.Client.BatchV1().CronJobs(namespace).List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, cronjob := range page.Items {
			nativeIDs = append(nativeIDs, prov.NativeID(cronjob.Namespace, cronjob.Name))
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list cronjobs: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

