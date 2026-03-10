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

const ResourceTypeConfigMap = "K8S::Core::ConfigMap"

func init() {
	registry.Register(
		ResourceTypeConfigMap,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &ConfigMap{Client: client, Config: cfg}
		},
	)
}

// ConfigMap implements the provisioner for K8S::Core::ConfigMap resources.
type ConfigMap struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &ConfigMap{}

func (c *ConfigMap) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var cm *v1coreac.ConfigMapApplyConfiguration
	if err := json.Unmarshal(request.Properties, &cm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configmap properties: %w", err)
	}

	namespace := c.Config.EffectiveNamespace()
	if cm.Namespace != nil {
		namespace = *cm.Namespace
	}

	result, err := c.Client.CoreV1().ConfigMaps(namespace).Apply(ctx, cm, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply configmap: %w", err)
	}

	ext, err := v1coreac.ExtractConfigMap(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract configmap: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal configmap properties: %w", err)
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

func (c *ConfigMap) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := c.Client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get configmap: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.ConfigMapApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get configmap live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (c *ConfigMap) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var cm *v1coreac.ConfigMapApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &cm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configmap properties: %w", err)
	}

	namespace := c.Config.EffectiveNamespace()
	if cm.Namespace != nil {
		namespace = *cm.Namespace
	}

	result, err := c.Client.CoreV1().ConfigMaps(namespace).Apply(ctx, cm, metav1.ApplyOptions{
		FieldManager: "formae",
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply configmap: %w", err)
	}

	ext, err := v1coreac.ExtractConfigMap(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract configmap: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal configmap properties: %w", err)
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

func (c *ConfigMap) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := c.Client.CoreV1().ConfigMaps(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete configmap: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (c *ConfigMap) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := c.Client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get configmap status: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.ConfigMapApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get configmap live state: %w", err)
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

func (c *ConfigMap) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := c.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := c.Client.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list configmaps: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, cm := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(cm.Namespace, cm.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
