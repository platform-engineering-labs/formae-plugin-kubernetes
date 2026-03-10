// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package core

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
	v1coreac "k8s.io/client-go/applyconfigurations/core/v1"
)

const ResourceTypeResourceQuota = "K8S::Core::ResourceQuota"

func init() {
	registry.Register(
		ResourceTypeResourceQuota,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &ResourceQuota{Client: client, Config: cfg}
		},
	)
}

// ResourceQuota implements the provisioner for K8S::Core::ResourceQuota resources.
type ResourceQuota struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &ResourceQuota{}

func (r *ResourceQuota) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var rq *v1coreac.ResourceQuotaApplyConfiguration
	if err := json.Unmarshal(request.Properties, &rq); err != nil {
		return nil, fmt.Errorf("failed to unmarshal resourcequota properties: %w", err)
	}

	namespace := r.Config.EffectiveNamespace()
	if rq.Namespace != nil {
		namespace = *rq.Namespace
	}

	result, err := r.Client.CoreV1().ResourceQuotas(namespace).Apply(ctx, rq, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply resourcequota: %w", err)
	}

	ext, err := v1coreac.ExtractResourceQuota(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract resourcequota: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal resourcequota properties: %w", err)
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

func (r *ResourceQuota) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := r.Client.CoreV1().ResourceQuotas(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get resourcequota: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.ResourceQuotaApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get resourcequota live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (r *ResourceQuota) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var rq *v1coreac.ResourceQuotaApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &rq); err != nil {
		return nil, fmt.Errorf("failed to unmarshal resourcequota properties: %w", err)
	}

	namespace := r.Config.EffectiveNamespace()
	if rq.Namespace != nil {
		namespace = *rq.Namespace
	}

	result, err := r.Client.CoreV1().ResourceQuotas(namespace).Apply(ctx, rq, metav1.ApplyOptions{
		FieldManager: "formae",
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply resourcequota: %w", err)
	}

	ext, err := v1coreac.ExtractResourceQuota(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract resourcequota: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal resourcequota properties: %w", err)
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

func (r *ResourceQuota) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := r.Client.CoreV1().ResourceQuotas(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete resourcequota: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (r *ResourceQuota) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := r.Client.CoreV1().ResourceQuotas(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get resourcequota status: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.ResourceQuotaApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get resourcequota live state: %w", err)
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

func (r *ResourceQuota) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := r.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := r.Client.CoreV1().ResourceQuotas(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list resourcequotas: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, rq := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(rq.Namespace, rq.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
