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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1coreac "k8s.io/client-go/applyconfigurations/core/v1"
)

const ResourceTypeEndpoints = "K8S::Core::Endpoints"

func init() {
	registry.Register(
		ResourceTypeEndpoints,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Endpoints{Client: client, Config: cfg}
		},
	)
}

// Endpoints implements the provisioner for K8S::Core::Endpoints resources.
type Endpoints struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Endpoints{}

func (e *Endpoints) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var ep *v1coreac.EndpointsApplyConfiguration //nolint:staticcheck // Endpoints resource intentionally supported
	if err := json.Unmarshal(request.Properties, &ep); err != nil {
		return nil, fmt.Errorf("failed to unmarshal endpoints properties: %w", err)
	}

	namespace := e.Config.EffectiveNamespace()
	if ep.Namespace != nil {
		namespace = *ep.Namespace
	}

	result, err := e.Client.CoreV1().Endpoints(namespace).Apply(ctx, ep, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply endpoints: %w", err)
	}

	ext, err := v1coreac.ExtractEndpoints(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract endpoints: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal endpoints properties: %w", err)
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

func (e *Endpoints) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := e.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get endpoints: %w", err)
	}
	if result == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	ext, err := v1coreac.ExtractEndpoints(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract endpoints: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal endpoints properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (e *Endpoints) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var ep *v1coreac.EndpointsApplyConfiguration //nolint:staticcheck // Endpoints resource intentionally supported
	if err := json.Unmarshal(request.DesiredProperties, &ep); err != nil {
		return nil, fmt.Errorf("failed to unmarshal endpoints properties: %w", err)
	}

	namespace := e.Config.EffectiveNamespace()
	if ep.Namespace != nil {
		namespace = *ep.Namespace
	}

	result, err := e.Client.CoreV1().Endpoints(namespace).Apply(ctx, ep, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply endpoints: %w", err)
	}

	ext, err := v1coreac.ExtractEndpoints(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract endpoints: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal endpoints properties: %w", err)
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

func (e *Endpoints) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ep, err := e.findByUID(ctx, request.NativeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to find endpoints: %w", err)
	}
	if ep == nil {
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			},
		}, nil
	}

	err = e.Client.CoreV1().Endpoints(ep.Namespace).Delete(ctx, ep.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete endpoints: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (e *Endpoints) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := e.findByUID(ctx, request.NativeID)
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
		return nil, fmt.Errorf("failed to get endpoints status: %w", err)
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

	ext, err := v1coreac.ExtractEndpoints(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract endpoints: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal endpoints properties: %w", err)
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

func (e *Endpoints) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := e.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := e.Client.CoreV1().Endpoints(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list endpoints: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, ep := range result.Items {
		nativeIDs = append(nativeIDs, string(ep.UID))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// findByUID finds an endpoints resource by its UID across all namespaces.
func (e *Endpoints) findByUID(ctx context.Context, uid string) (*v1.Endpoints, error) { //nolint:staticcheck // Endpoints resource intentionally supported
	list, err := e.Client.CoreV1().Endpoints(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
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
