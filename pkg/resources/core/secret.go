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

// enrichDecodedData attaches the secret's base64-decoded payload as a read-only
// `decodedData` field on the live-state properties. The Kubernetes API returns
// secret values base64-encoded on the wire, but client-go decodes them into
// Secret.Data (map[string][]byte), so the raw bytes are already the plaintext
// value and are converted to strings verbatim (no second decode).
//
// decodedData is deliberately separate from `data`: `data` round-trips as
// base64 on Create/Update, so decoding it in place would make actual state
// diverge from desired and produce perpetual drift. The schema types
// decodedData with formae.SecretValue and marks it writeOnly, so the agent
// hashes it at rest and excludes it from drift detection; consumers reference a
// value with `secret.res.secretValue.at("key")`.
func enrichDecodedData(properties []byte, data map[string][]byte) ([]byte, error) {
	if len(data) == 0 {
		return properties, nil
	}
	var props map[string]any
	if err := json.Unmarshal(properties, &props); err != nil {
		return nil, fmt.Errorf("failed to unmarshal secret properties for decode enrichment: %w", err)
	}
	decoded := make(map[string]string, len(data))
	for k, v := range data {
		decoded[k] = string(v)
	}
	props["decodedData"] = decoded
	return json.Marshal(props)
}

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
	if err := prov.UnmarshalApplyConfig(request.Properties, &secret); err != nil {
		return nil, fmt.Errorf("failed to unmarshal secret properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(secret.Namespace, ResourceTypeSecret)
	if err != nil {
		return nil, err
	}

	result, err := s.Client.CoreV1().Secrets(namespace).Apply(ctx, secret, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply secret: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.SecretApplyConfiguration](result, "Secret", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get secret live state: %w", err)
	}
	properties, err = enrichDecodedData(properties, result.Data)
	if err != nil {
		return nil, err
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

func (s *Secret) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	properties, err := prov.LiveState[v1coreac.SecretApplyConfiguration](result, "Secret", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get secret live state: %w", err)
	}
	properties, err = enrichDecodedData(properties, result.Data)
	if err != nil {
		return nil, err
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (s *Secret) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var secret *v1coreac.SecretApplyConfiguration
	if err := prov.UnmarshalApplyConfig(request.DesiredProperties, &secret); err != nil {
		return nil, fmt.Errorf("failed to unmarshal secret properties: %w", err)
	}

	namespace, err := prov.ResolveCreateNamespace(secret.Namespace, ResourceTypeSecret)
	if err != nil {
		return nil, err
	}

	result, err := s.Client.CoreV1().Secrets(namespace).Apply(ctx, secret, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply secret: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, secret, func(name string, patch []byte, opts metav1.PatchOptions) error {
		_, err := s.Client.CoreV1().Secrets(namespace).Patch(ctx, name, types.MergePatchType, patch, opts)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile secret metadata: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.SecretApplyConfiguration](result, "Secret", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get secret live state: %w", err)
	}
	properties, err = enrichDecodedData(properties, result.Data)
	if err != nil {
		return nil, err
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
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
	err = s.Client.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{})
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
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}
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

	properties, err := prov.LiveState[v1coreac.SecretApplyConfiguration](result, "Secret", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get secret live state: %w", err)
	}
	properties, err = enrichDecodedData(properties, result.Data)
	if err != nil {
		return nil, err
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
	namespace, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeSecret)
	if err != nil {
		return nil, err
	}

	var nativeIDs []string
	if err := prov.EachPage(ctx, func(ctx context.Context, opts metav1.ListOptions) (string, error) {
		page, err := s.Client.CoreV1().Secrets(namespace).List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, secret := range page.Items {
			nativeIDs = append(nativeIDs, prov.NativeID(secret.Namespace, secret.Name))
		}
		return page.Continue, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
