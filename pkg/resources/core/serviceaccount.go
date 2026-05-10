// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

// stripLegacyTokenSecrets drops auto-generated token Secret refs from a
// ServiceAccount's `.secrets` array. Pre-K8s-1.24, the
// service-account-controller appends `{name: "<sa-name>-token-xxxxx"}`
// to every SA. The controller (not formae) is the field manager, but
// the entry leaks through extract and shows up as drift on Sync.
// On K8s 1.24+ this is a no-op (LegacyServiceAccountTokenNoAutoGeneration
// is GA — controller doesn't auto-create the token Secret entry).
func stripLegacyTokenSecrets(propsJSON []byte, saName string) ([]byte, error) {
	if len(propsJSON) == 0 {
		return propsJSON, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(propsJSON, &doc); err != nil {
		return propsJSON, nil
	}
	raw, ok := doc["secrets"].([]any)
	if !ok || len(raw) == 0 {
		return propsJSON, nil
	}
	prefix := saName + "-token-"
	kept := raw[:0]
	for _, e := range raw {
		entry, ok := e.(map[string]any)
		if !ok {
			kept = append(kept, e)
			continue
		}
		name, _ := entry["name"].(string)
		if strings.HasPrefix(name, prefix) {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == 0 {
		delete(doc, "secrets")
	} else {
		doc["secrets"] = kept
	}
	return json.Marshal(doc)
}

const ResourceTypeServiceAccount = "K8S::Core::ServiceAccount"

func init() {
	registry.Register(
		ResourceTypeServiceAccount,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &ServiceAccount{Client: client, Config: cfg}
		},
	)
}

// ServiceAccount implements the provisioner for K8S::Core::ServiceAccount resources.
type ServiceAccount struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &ServiceAccount{}

func (sa *ServiceAccount) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var svcAcct *v1coreac.ServiceAccountApplyConfiguration
	if err := json.Unmarshal(request.Properties, &svcAcct); err != nil {
		return nil, fmt.Errorf("failed to unmarshal serviceaccount properties: %w", err)
	}

	namespace := sa.Config.EffectiveNamespace()
	if svcAcct.Namespace != nil {
		namespace = *svcAcct.Namespace
	}

	result, err := sa.Client.CoreV1().ServiceAccounts(namespace).Apply(ctx, svcAcct, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply serviceaccount: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.ServiceAccountApplyConfiguration](result, "ServiceAccount", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get serviceaccount live state: %w", err)
	}
	properties, err = stripLegacyTokenSecrets(properties, result.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to strip legacy token secrets: %w", err)
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

func (sa *ServiceAccount) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := sa.Client.CoreV1().ServiceAccounts(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get serviceaccount: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.ServiceAccountApplyConfiguration](result, "ServiceAccount", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get serviceaccount live state: %w", err)
	}
	properties, err = stripLegacyTokenSecrets(properties, result.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to strip legacy token secrets: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (sa *ServiceAccount) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var svcAcct *v1coreac.ServiceAccountApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &svcAcct); err != nil {
		return nil, fmt.Errorf("failed to unmarshal serviceaccount properties: %w", err)
	}

	namespace := sa.Config.EffectiveNamespace()
	if svcAcct.Namespace != nil {
		namespace = *svcAcct.Namespace
	}

	result, err := sa.Client.CoreV1().ServiceAccounts(namespace).Apply(ctx, svcAcct, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply serviceaccount: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, svcAcct, func(name string, patch []byte) error {
		_, err := sa.Client.CoreV1().ServiceAccounts(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile serviceaccount metadata: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.ServiceAccountApplyConfiguration](result, "ServiceAccount", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get serviceaccount live state: %w", err)
	}
	properties, err = stripLegacyTokenSecrets(properties, result.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to strip legacy token secrets: %w", err)
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

func (sa *ServiceAccount) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := sa.Client.CoreV1().ServiceAccounts(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete serviceaccount: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (sa *ServiceAccount) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := sa.Client.CoreV1().ServiceAccounts(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get serviceaccount status: %w", err)
	}

	properties, err := prov.LiveState[v1coreac.ServiceAccountApplyConfiguration](result, "ServiceAccount", "v1")
	if err != nil {
		return nil, fmt.Errorf("failed to get serviceaccount live state: %w", err)
	}
	properties, err = stripLegacyTokenSecrets(properties, result.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to strip legacy token secrets: %w", err)
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

func (sa *ServiceAccount) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := sa.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := sa.Client.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list serviceaccounts: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, svcAcct := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(svcAcct.Namespace, svcAcct.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
