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

const ResourceTypeJob = "K8S::Batch::Job"

func init() {
	registry.Register(
		ResourceTypeJob,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Job{Client: client, Config: cfg}
		},
	)
}

// Job implements the provisioner for K8S::Batch::Job resources.
type Job struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Job{}

func (j *Job) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var job *batchv1ac.JobApplyConfiguration
	if err := json.Unmarshal(request.Properties, &job); err != nil {
		return nil, fmt.Errorf("failed to unmarshal job properties: %w", err)
	}

	namespace := j.Config.EffectiveNamespace()
	if job.Namespace != nil {
		namespace = *job.Namespace
	}

	result, err := j.Client.BatchV1().Jobs(namespace).Apply(ctx, job, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply job: %w", err)
	}

	ext, err := batchv1ac.ExtractJob(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract job: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal job properties: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    j.fromConditions(result.Status.Conditions),
			RequestID:          fmt.Sprintf("%d", result.Generation),
			StatusMessage:      j.statusMessage(result.Status),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (j *Job) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := j.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get job: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := batchv1ac.ExtractJob(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract job: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal job properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (j *Job) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var job *batchv1ac.JobApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &job); err != nil {
		return nil, fmt.Errorf("failed to unmarshal job properties: %w", err)
	}

	namespace := j.Config.EffectiveNamespace()
	if job.Namespace != nil {
		namespace = *job.Namespace
	}

	result, err := j.Client.BatchV1().Jobs(namespace).Apply(ctx, job, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply job: %w", err)
	}

	ext, err := batchv1ac.ExtractJob(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract job: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal job properties: %w", err)
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    j.fromConditions(result.Status.Conditions),
			RequestID:          result.ResourceVersion,
			StatusMessage:      j.statusMessage(result.Status),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (j *Job) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	job, err := j.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find job: %w", err)
	}
	if job == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	// Use propagation policy to clean up child pods
	propagation := metav1.DeletePropagationBackground
	err = j.Client.BatchV1().Jobs(job.Namespace).Delete(ctx, job.Name, metav1.DeleteOptions{
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
		return nil, fmt.Errorf("failed to delete job: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (j *Job) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := j.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get job status: %w", err)
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

	ext, err := batchv1ac.ExtractJob(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract job: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal job properties: %w", err)
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    j.fromConditions(result.Status.Conditions),
			RequestID:          request.RequestID,
			StatusMessage:      j.statusMessage(result.Status),
			NativeID:           string(result.UID),
			ResourceProperties: properties,
		},
	}, nil
}

func (j *Job) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := j.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := j.Client.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, job := range result.Items {
		nativeIDs = append(nativeIDs, string(job.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds a job by its UID across all namespaces.
func (j *Job) findByUID(ctx context.Context, uid string) (*batchv1.Job, error) {
	list, err := j.Client.BatchV1().Jobs(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
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

// fromConditions maps K8S Job conditions to Formae OperationStatus.
func (j *Job) fromConditions(conditions []batchv1.JobCondition) resource.OperationStatus {
	for _, cond := range conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == "True" {
			return resource.OperationStatusFailure
		}
		if cond.Type == batchv1.JobComplete && cond.Status == "True" {
			return resource.OperationStatusSuccess
		}
		if cond.Type == batchv1.JobSuspended && cond.Status == "True" {
			return resource.OperationStatusSuccess
		}
	}
	return resource.OperationStatusInProgress
}

// statusMessage builds a status message from Job status.
func (j *Job) statusMessage(status batchv1.JobStatus) string {
	return fmt.Sprintf("active: %d, succeeded: %d, failed: %d",
		status.Active, status.Succeeded, status.Failed)
}
