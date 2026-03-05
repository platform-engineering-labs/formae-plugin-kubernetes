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

const ResourceTypeLimitRange = "K8S::Core::LimitRange"

func init() {
	registry.Register(
		ResourceTypeLimitRange,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &LimitRange{Client: client, Config: cfg}
		},
	)
}

// LimitRange implements the provisioner for K8S::Core::LimitRange resources.
type LimitRange struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &LimitRange{}

func (l *LimitRange) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var lr *v1coreac.LimitRangeApplyConfiguration
	if err := json.Unmarshal(request.Properties, &lr); err != nil {
		return nil, fmt.Errorf("failed to unmarshal limitrange properties: %w", err)
	}

	namespace := l.Config.EffectiveNamespace()
	if lr.Namespace != nil {
		namespace = *lr.Namespace
	}

	result, err := l.Client.CoreV1().LimitRanges(namespace).Apply(ctx, lr, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply limitrange: %w", err)
	}

	ext, err := v1coreac.ExtractLimitRange(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract limitrange: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal limitrange properties: %w", err)
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

func (l *LimitRange) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := l.Client.CoreV1().LimitRanges(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get limitrange: %w", err)
	}

	ext, err := v1coreac.ExtractLimitRange(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract limitrange: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal limitrange properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (l *LimitRange) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var lr *v1coreac.LimitRangeApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &lr); err != nil {
		return nil, fmt.Errorf("failed to unmarshal limitrange properties: %w", err)
	}

	namespace := l.Config.EffectiveNamespace()
	if lr.Namespace != nil {
		namespace = *lr.Namespace
	}

	result, err := l.Client.CoreV1().LimitRanges(namespace).Apply(ctx, lr, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply limitrange: %w", err)
	}

	ext, err := v1coreac.ExtractLimitRange(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract limitrange: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal limitrange properties: %w", err)
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

func (l *LimitRange) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := l.Client.CoreV1().LimitRanges(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete limitrange: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (l *LimitRange) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := l.Client.CoreV1().LimitRanges(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get limitrange status: %w", err)
	}

	ext, err := v1coreac.ExtractLimitRange(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract limitrange: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal limitrange properties: %w", err)
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

func (l *LimitRange) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := l.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := l.Client.CoreV1().LimitRanges(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list limitranges: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, lr := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(lr.Namespace, lr.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
