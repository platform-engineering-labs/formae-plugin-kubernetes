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

const ResourceTypeSecret = "K8S::Core::Secret"

func init() {
	registry.Register(
		ResourceTypeSecret,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Secret{Client: client, Config: cfg}
		},
	)
}

// Secret implements the provisioner for K8S::Core::Secret resources.
type Secret struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Secret{}

func (s *Secret) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var secret *v1coreac.SecretApplyConfiguration
	if err := json.Unmarshal(request.Properties, &secret); err != nil {
		return nil, fmt.Errorf("failed to unmarshal secret properties: %w", err)
	}

	namespace := s.Config.EffectiveNamespace()
	if secret.Namespace != nil {
		namespace = *secret.Namespace
	}

	result, err := s.Client.CoreV1().Secrets(namespace).Apply(ctx, secret, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply secret: %w", err)
	}

	ext, err := v1coreac.ExtractSecret(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract secret: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal secret properties: %w", err)
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

func (s *Secret) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := s.Client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.SecretApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (s *Secret) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var secret *v1coreac.SecretApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &secret); err != nil {
		return nil, fmt.Errorf("failed to unmarshal secret properties: %w", err)
	}

	namespace := s.Config.EffectiveNamespace()
	if secret.Namespace != nil {
		namespace = *secret.Namespace
	}

	result, err := s.Client.CoreV1().Secrets(namespace).Apply(ctx, secret, metav1.ApplyOptions{
		FieldManager: "formae",
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply secret: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, secret, func(name string, patch []byte) error {
		_, err := s.Client.CoreV1().Secrets(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile secret metadata: %w", err)
	}

	ext, err := v1coreac.ExtractSecret(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract secret: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal secret properties: %w", err)
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

func (s *Secret) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := s.Client.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete secret: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (s *Secret) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := s.Client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get secret status: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.SecretApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret live state: %w", err)
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

func (s *Secret) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := s.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := s.Client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, secret := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(secret.Namespace, secret.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
