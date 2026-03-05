// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package generic

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// CRDInfo describes a custom resource type for the generic provisioner.
type CRDInfo struct {
	GVR        schema.GroupVersionResource
	Namespaced bool
}

var (
	mu       sync.RWMutex
	crdInfos = make(map[string]*CRDInfo)
)

// RegisterCRD registers a CRD with the generic provisioner and the global registry.
func RegisterCRD(resourceType string, info CRDInfo, operations []resource.Operation) {
	mu.Lock()
	crdInfos[resourceType] = &info
	mu.Unlock()

	registry.Register(resourceType, operations, func(client *transport.Client, cfg *config.Config) prov.Provisioner {
		return &CustomResource{
			Client:       client,
			Config:       cfg,
			ResourceType: resourceType,
			Info:         &info,
		}
	})
}

// CustomResource implements prov.Provisioner for any CRD using the dynamic client.
type CustomResource struct {
	Client       *transport.Client
	Config       *config.Config
	ResourceType string
	Info         *CRDInfo
}

var _ prov.Provisioner = &CustomResource{}

func (cr *CustomResource) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	obj, err := cr.unmarshalObject(request.Properties)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s properties: %w", cr.ResourceType, err)
	}

	namespace := cr.resolveNamespace(obj)

	result, err := cr.applySSA(ctx, namespace, obj)
	if err != nil {
		return nil, fmt.Errorf("failed to apply %s: %w", cr.ResourceType, err)
	}

	properties, err := cr.marshalClean(result)
	if err != nil {
		return nil, err
	}

	nativeID := result.GetName()
	if cr.Info.Namespaced {
		nativeID = prov.NativeID(result.GetNamespace(), result.GetName())
	}

	return &resource.CreateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.GetResourceVersion(),
			NativeID:           nativeID,
			ResourceProperties: properties,
		},
	}, nil
}

func (cr *CustomResource) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)

	var result *unstructured.Unstructured
	var err error
	if cr.Info.Namespaced {
		result, err = cr.Client.Dynamic.Resource(cr.Info.GVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	} else {
		result, err = cr.Client.Dynamic.Resource(cr.Info.GVR).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return &resource.ReadResult{
				ResourceType: request.ResourceType,
				ErrorCode:    resource.OperationErrorCodeNotFound,
			}, nil
		}
		return nil, fmt.Errorf("failed to get %s: %w", cr.ResourceType, err)
	}

	properties, err := cr.marshalClean(result)
	if err != nil {
		return nil, err
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(properties),
	}, nil
}

func (cr *CustomResource) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	obj, err := cr.unmarshalObject(request.DesiredProperties)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s properties: %w", cr.ResourceType, err)
	}

	namespace := cr.resolveNamespace(obj)

	result, err := cr.applySSA(ctx, namespace, obj)
	if err != nil {
		return nil, fmt.Errorf("failed to apply %s: %w", cr.ResourceType, err)
	}

	properties, err := cr.marshalClean(result)
	if err != nil {
		return nil, err
	}

	nativeID := result.GetName()
	if cr.Info.Namespaced {
		nativeID = prov.NativeID(result.GetNamespace(), result.GetName())
	}

	return &resource.UpdateResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          result.GetResourceVersion(),
			NativeID:           nativeID,
			ResourceProperties: properties,
		},
	}, nil
}

func (cr *CustomResource) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)

	var err error
	if cr.Info.Namespaced {
		err = cr.Client.Dynamic.Resource(cr.Info.GVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	} else {
		err = cr.Client.Dynamic.Resource(cr.Info.GVR).Delete(ctx, name, metav1.DeleteOptions{})
	}
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusSuccess,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to delete %s: %w", cr.ResourceType, err)
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		},
	}, nil
}

func (cr *CustomResource) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	ns, name := prov.ParseNativeID(request.NativeID)

	var result *unstructured.Unstructured
	var err error
	if cr.Info.Namespaced {
		result, err = cr.Client.Dynamic.Resource(cr.Info.GVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	} else {
		result, err = cr.Client.Dynamic.Resource(cr.Info.GVR).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return &resource.StatusResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationCheckStatus,
					OperationStatus: resource.OperationStatusFailure,
					ErrorCode:       resource.OperationErrorCodeNotFound,
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to get %s status: %w", cr.ResourceType, err)
	}

	properties, err := cr.marshalClean(result)
	if err != nil {
		return nil, err
	}

	nativeID := result.GetName()
	if cr.Info.Namespaced {
		nativeID = prov.NativeID(result.GetNamespace(), result.GetName())
	}

	return &resource.StatusResult{
		ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          request.RequestID,
			NativeID:           nativeID,
			ResourceProperties: properties,
		},
	}, nil
}

func (cr *CustomResource) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	var list *unstructured.UnstructuredList
	var err error

	if cr.Info.Namespaced {
		namespace := cr.Config.EffectiveNamespace()
		if ns, ok := request.AdditionalProperties["namespace"]; ok && ns != "" {
			namespace = ns
		}
		list, err = cr.Client.Dynamic.Resource(cr.Info.GVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = cr.Client.Dynamic.Resource(cr.Info.GVR).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", cr.ResourceType, err)
	}

	nativeIDs := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		if cr.Info.Namespaced {
			nativeIDs = append(nativeIDs, prov.NativeID(item.GetNamespace(), item.GetName()))
		} else {
			nativeIDs = append(nativeIDs, item.GetName())
		}
	}

	return &resource.ListResult{
		NativeIDs: nativeIDs,
	}, nil
}

// applySSA performs Server-Side Apply using the dynamic client.
func (cr *CustomResource) applySSA(ctx context.Context, namespace string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal object for SSA: %w", err)
	}

	opts := metav1.PatchOptions{FieldManager: "formae"}

	if cr.Info.Namespaced {
		return cr.Client.Dynamic.Resource(cr.Info.GVR).Namespace(namespace).Patch(ctx, obj.GetName(), types.ApplyPatchType, data, opts)
	}
	return cr.Client.Dynamic.Resource(cr.Info.GVR).Patch(ctx, obj.GetName(), types.ApplyPatchType, data, opts)
}

// resolveNamespace extracts the namespace from the object or falls back to config.
func (cr *CustomResource) resolveNamespace(obj *unstructured.Unstructured) string {
	if !cr.Info.Namespaced {
		return ""
	}
	if ns := obj.GetNamespace(); ns != "" {
		return ns
	}
	return cr.Config.EffectiveNamespace()
}

// unmarshalObject unmarshals JSON properties into an Unstructured object.
func (cr *CustomResource) unmarshalObject(data json.RawMessage) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &obj.Object); err != nil {
		return nil, err
	}
	return obj, nil
}

// marshalClean marshals an Unstructured object after stripping K8S-managed fields.
func (cr *CustomResource) marshalClean(obj *unstructured.Unstructured) (json.RawMessage, error) {
	clean := obj.DeepCopy()
	stripManagedFields(clean)

	data, err := json.Marshal(clean.Object)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s properties: %w", cr.ResourceType, err)
	}
	return data, nil
}

// stripManagedFields removes K8S-managed fields from the object so that only
// user-specified fields remain in the returned properties.
func stripManagedFields(obj *unstructured.Unstructured) {
	delete(obj.Object, "status")

	metadata, ok := obj.Object["metadata"].(map[string]any)
	if !ok {
		return
	}

	delete(metadata, "managedFields")
	delete(metadata, "creationTimestamp")
	delete(metadata, "generation")
	delete(metadata, "resourceVersion")
	delete(metadata, "uid")
	delete(metadata, "selfLink")
}
