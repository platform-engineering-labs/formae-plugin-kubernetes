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
	"k8s.io/apimachinery/pkg/types"
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

	namespace, err := prov.ResolveCreateNamespace(lr.Namespace, ResourceTypeLimitRange)
	if err != nil {
		return nil, err
	}

	result, err := l.Client.CoreV1().LimitRanges(namespace).Apply(ctx, lr, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply limitrange: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.LimitRangeApplyConfiguration](result, "LimitRange", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get limitrange live state: %w", err)
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.ResourceVersion,
			NativeID:           prov.NativeID(result.Namespace, result.Name),
			ResourceProperties: properties,
		},
	}, nil
}

func (l *LimitRange) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	properties, err := prov.LiveState[v1coreac.LimitRangeApplyConfiguration](result, "LimitRange", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get limitrange live state: %w", err)
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

	namespace, err := prov.ResolveCreateNamespace(lr.Namespace, ResourceTypeLimitRange)
	if err != nil {
		return nil, err
	}

	result, err := l.Client.CoreV1().LimitRanges(namespace).Apply(ctx, lr, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply limitrange: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, lr, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := l.Client.CoreV1().LimitRanges(namespace).Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile limitrange metadata: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.LimitRangeApplyConfiguration](result, "LimitRange", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get limitrange live state: %w", err)
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
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = l.Client.CoreV1().LimitRanges(ns).Delete(ctx, name, metav1.DeleteOptions{})
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
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	properties, err := prov.LiveState[v1coreac.LimitRangeApplyConfiguration](result, "LimitRange", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get limitrange live state: %w", err)
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
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeLimitRange)
	if err != nil {
		return nil, err
	}

	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := l.Client.CoreV1().LimitRanges(namespace).List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, lr := range page.Items {
			nativeIDs = append(nativeIDs, prov.NativeID(lr.Namespace, lr.Name))
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list limitranges: %w", err)
	}


	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
