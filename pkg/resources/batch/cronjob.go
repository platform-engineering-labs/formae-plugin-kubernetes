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
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	namespace := cj.Config.EffectiveNamespace()
	if cronjob.Namespace != nil {
		namespace = *cronjob.Namespace
	}

	result, err := cj.Client.BatchV1().CronJobs(namespace).Apply(ctx, cronjob, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply cronjob: %w", err)
	}

	ext, err := batchv1ac.ExtractCronJob(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract cronjob: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cronjob properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          fmt.Sprintf("%d", result.Generation),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (cj *CronJob) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := cj.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get cronjob: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := batchv1ac.ExtractCronJob(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract cronjob: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cronjob properties: %w", err)
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

	namespace := cj.Config.EffectiveNamespace()
	if cronjob.Namespace != nil {
		namespace = *cronjob.Namespace
	}

	result, err := cj.Client.BatchV1().CronJobs(namespace).Apply(ctx, cronjob, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply cronjob: %w", err)
	}

	ext, err := batchv1ac.ExtractCronJob(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract cronjob: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cronjob properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (cj *CronJob) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	cronjob, err := cj.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find cronjob: %w", err)
	}
	if cronjob == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = cj.Client.BatchV1().CronJobs(cronjob.Namespace).Delete(ctx, cronjob.Name, metav1.DeleteOptions{})
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
	result, err := cj.findByUID(ctx, request.NativeID)
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
	if result == nil {
		return &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCheckStatus,
				OperationStatus: resource.OperationStatusFailure,
				ErrorCode:       resource.OperationErrorCodeNotFound,
			},
		}, nil
	}

	ext, err := batchv1ac.ExtractCronJob(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract cronjob: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cronjob properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (cj *CronJob) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := cj.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := cj.Client.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list cronjobs: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, cronjob := range result.Items {
		nativeIDs = append(nativeIDs, string(cronjob.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a cronjob by its UID across all namespaces.
func (cj *CronJob) findByUID(ctx context.Context, uid string) (*batchv1.CronJob, error) {
	list, err := cj.Client.BatchV1().CronJobs(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
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
