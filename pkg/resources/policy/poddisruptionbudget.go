// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package policy

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
	policyv1ac "k8s.io/client-go/applyconfigurations/policy/v1"
)

const ResourceTypePodDisruptionBudget = "K8S::Policy::PodDisruptionBudget"

func init() {
	registry.Register(
		ResourceTypePodDisruptionBudget,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &PodDisruptionBudget{Client: client, Config: cfg}
		},
	)
}

// PodDisruptionBudget implements the provisioner for K8S::Policy::PodDisruptionBudget resources.
type PodDisruptionBudget struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &PodDisruptionBudget{}

func (p *PodDisruptionBudget) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	var pdb *policyv1ac.PodDisruptionBudgetApplyConfiguration
	if err := json.Unmarshal(request.Properties, &pdb); err != nil {
		return nil, fmt.Errorf("failed to unmarshal poddisruptionbudget properties: %w", err)
	}

	namespace := p.Config.EffectiveNamespace()
	if pdb.Namespace != nil {
		namespace = *pdb.Namespace
	}

	result, err := p.Client.PolicyV1().PodDisruptionBudgets(namespace).Apply(ctx, pdb, metav1.ApplyOptions{
		FieldManager: "formae",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply poddisruptionbudget: %w", err)
	}

	ext, err := policyv1ac.ExtractPodDisruptionBudget(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract poddisruptionbudget: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal poddisruptionbudget properties: %w", err)
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

func (p *PodDisruptionBudget) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := p.Client.PolicyV1().PodDisruptionBudgets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get poddisruptionbudget: %w", err)
	}

	properties, err := prov.LiveState[policyv1ac.PodDisruptionBudgetApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get poddisruptionbudget live state: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (p *PodDisruptionBudget) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var pdb *policyv1ac.PodDisruptionBudgetApplyConfiguration
	if err := json.Unmarshal(request.DesiredProperties, &pdb); err != nil {
		return nil, fmt.Errorf("failed to unmarshal poddisruptionbudget properties: %w", err)
	}

	namespace := p.Config.EffectiveNamespace()
	if pdb.Namespace != nil {
		namespace = *pdb.Namespace
	}

	result, err := p.Client.PolicyV1().PodDisruptionBudgets(namespace).Apply(ctx, pdb, metav1.ApplyOptions{
		FieldManager: "formae",
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply poddisruptionbudget: %w", err)
	}

	// Reconcile metadata: remove labels/annotations not in desired state.
	if err := prov.ReconcileMetadata(result, pdb, func(name string, patch []byte) error {
		_, err := p.Client.PolicyV1().PodDisruptionBudgets(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to reconcile poddisruptionbudget metadata: %w", err)
	}

	ext, err := policyv1ac.ExtractPodDisruptionBudget(result, "formae")
	if err != nil {
		return nil, fmt.Errorf("failed to extract poddisruptionbudget: %w", err)
	}

	properties, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal poddisruptionbudget properties: %w", err)
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

func (p *PodDisruptionBudget) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	err := p.Client.PolicyV1().PodDisruptionBudgets(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete poddisruptionbudget: %w", err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (p *PodDisruptionBudget) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)
	result, err := p.Client.PolicyV1().PodDisruptionBudgets(ns).Get(ctx, name, metav1.GetOptions{})
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
		return nil, fmt.Errorf("failed to get poddisruptionbudget status: %w", err)
	}

	properties, err := prov.LiveState[policyv1ac.PodDisruptionBudgetApplyConfiguration](result)
	if err != nil {
		return nil, fmt.Errorf("failed to get poddisruptionbudget live state: %w", err)
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

func (p *PodDisruptionBudget) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	namespace := p.Config.EffectiveNamespace()
	if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
		namespace = ns
	}

	result, err := p.Client.PolicyV1().PodDisruptionBudgets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list poddisruptionbudgets: %w", err)
	}

	nativeIDs := make([]string, 0, len(result.Items))
	for _, pdb := range result.Items {
		nativeIDs = append(nativeIDs, prov.NativeID(pdb.Namespace, pdb.Name))
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}
