// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package custom

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// ResourceTypeCustom is the single catch-all type for every custom resource and
// any K8s kind without a typed provisioner (including CustomResourceDefinition).
const ResourceTypeCustom = "K8S::Custom::Resource"

func init() {
	registry.Register(
		ResourceTypeCustom,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &CustomResource{Client: client, Config: cfg}
		},
	)
}

// CustomResource is the generic dynamic provisioner.
type CustomResource struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &CustomResource{}

// crdEstablishTimeout bounds how long a Create/Update waits for a custom
// resource's kind to become resolvable. When a forma declares a CRD and an
// instance of it together, formae may apply them concurrently; the instance's
// kind is not servable until the apiserver establishes the freshly-created
// CRD. We retry GVR resolution (each attempt resets the RESTMapper) so the
// instance Create converges once the CRD is established, instead of failing
// the whole apply on a transient "no REST mapping" miss.
const (
	crdEstablishTimeout = 30 * time.Second
	crdEstablishBackoff = 500 * time.Millisecond
)

// parseManifest normalizes incoming property JSON, strips formae-only keys,
// and returns the unstructured object plus its resolved GVR and scope.
func (c *CustomResource) parseManifest(ctx context.Context, raw []byte) (*unstructured.Unstructured, schema.GroupVersionResource, bool, error) {
	normalized, err := prov.NormalizeMetadata(raw)
	if err != nil {
		return nil, schema.GroupVersionResource{}, false, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(normalized, &m); err != nil {
		return nil, schema.GroupVersionResource{}, false, fmt.Errorf("unmarshal custom manifest: %w", err)
	}
	// formaeId is a formae-only identity field, never sent to the apiserver.
	delete(m, "formaeId")

	obj := &unstructured.Unstructured{Object: m}
	apiVersion := obj.GetAPIVersion()
	kind := obj.GetKind()
	if apiVersion == "" || kind == "" {
		return nil, schema.GroupVersionResource{}, false, fmt.Errorf("custom manifest missing apiVersion or kind")
	}
	gvr, namespaced, err := c.resolveMappingWithRetry(ctx, apiVersion, kind)
	if err != nil {
		return nil, schema.GroupVersionResource{}, false, err
	}
	return obj, gvr, namespaced, nil
}

// resolveMappingWithRetry resolves apiVersion+kind to a GVR, retrying on miss
// for up to crdEstablishTimeout to absorb the delay between a CRD being created
// and its kind becoming servable. Each attempt resets the RESTMapper.
func (c *CustomResource) resolveMappingWithRetry(ctx context.Context, apiVersion, kind string) (schema.GroupVersionResource, bool, error) {
	deadline := time.Now().Add(crdEstablishTimeout)
	for {
		gvr, namespaced, err := c.Client.ResolveMapping(apiVersion, kind)
		if err == nil {
			return gvr, namespaced, nil
		}
		if time.Now().After(deadline) {
			return schema.GroupVersionResource{}, false, err
		}
		select {
		case <-ctx.Done():
			return schema.GroupVersionResource{}, false, ctx.Err()
		case <-time.After(crdEstablishBackoff):
		}
	}
}

// resourceFor returns the dynamic client handle scoped correctly: namespaced
// kinds are bound to a namespace, cluster-scoped kinds are not.
func (c *CustomResource) resourceFor(gvr schema.GroupVersionResource, namespaced bool, namespace string) dynamic.ResourceInterface {
	if namespaced {
		return c.Client.Dynamic.Resource(gvr).Namespace(namespace)
	}
	return c.Client.Dynamic.Resource(gvr)
}

func (c *CustomResource) apply(ctx context.Context, raw []byte, op resource.Operation) (*resource.ProgressResult, error) {
	obj, gvr, namespaced, err := c.parseManifest(ctx, raw)
	if err != nil {
		return nil, err
	}
	ns := obj.GetNamespace()
	if namespaced && ns == "" {
		ns = "default"
		obj.SetNamespace(ns)
	}
	result, err := c.resourceFor(gvr, namespaced, ns).Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{
		FieldManager: prov.FieldManager,
		Force:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply %s/%s: %w", obj.GetAPIVersion(), obj.GetKind(), err)
	}
	properties, err := prov.CustomLiveState(result.Object)
	if err != nil {
		return nil, err
	}
	return &resource.ProgressResult{
		Operation:          op,
		OperationStatus:    resource.OperationStatusSuccess,
		RequestID:          result.GetResourceVersion(),
		NativeID:           prov.CustomResourceID(result.GetAPIVersion(), result.GetKind(), result.GetNamespace(), result.GetName()),
		ResourceProperties: properties,
	}, nil
}

func (c *CustomResource) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	pr, err := c.apply(ctx, request.Properties, resource.OperationCreate)
	if err != nil {
		return nil, err
	}
	return &resource.CreateResult{ProgressResult: pr}, nil
}

func (c *CustomResource) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	pr, err := c.apply(ctx, request.DesiredProperties, resource.OperationUpdate)
	if err != nil {
		return nil, err
	}
	return &resource.UpdateResult{ProgressResult: pr}, nil
}

// getByID resolves GVR from the NativeID and fetches the live object.
func (c *CustomResource) getByID(ctx context.Context, nativeID string) (*unstructured.Unstructured, error) {
	apiVersion, kind, ns, name, err := prov.ParseCustomResourceID(nativeID)
	if err != nil {
		return nil, err
	}
	gvr, namespaced, err := c.Client.ResolveMapping(apiVersion, kind)
	if err != nil {
		return nil, err
	}
	return c.resourceFor(gvr, namespaced, ns).Get(ctx, name, metav1.GetOptions{})
}

func (c *CustomResource) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := c.getByID(ctx, request.NativeID)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &resource.ReadResult{ResourceType: request.ResourceType, ErrorCode: resource.OperationErrorCodeNotFound}, nil
		}
		return nil, err
	}
	properties, err := prov.CustomLiveState(result.Object)
	if err != nil {
		return nil, err
	}
	return &resource.ReadResult{ResourceType: request.ResourceType, Properties: string(properties)}, nil
}

func (c *CustomResource) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := c.getByID(ctx, request.NativeID)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &resource.StatusResult{ProgressResult: &resource.ProgressResult{
				Operation: resource.OperationCheckStatus, OperationStatus: resource.OperationStatusFailure, ErrorCode: resource.OperationErrorCodeNotFound,
			}}, nil
		}
		return nil, err
	}
	properties, err := prov.CustomLiveState(result.Object)
	if err != nil {
		return nil, err
	}
	return &resource.StatusResult{ProgressResult: &resource.ProgressResult{
		Operation:          resource.OperationCheckStatus,
		OperationStatus:    resource.OperationStatusSuccess,
		RequestID:          request.RequestID,
		NativeID:           prov.CustomResourceID(result.GetAPIVersion(), result.GetKind(), result.GetNamespace(), result.GetName()),
		ResourceProperties: properties,
	}}, nil
}

func (c *CustomResource) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	apiVersion, kind, ns, name, err := prov.ParseCustomResourceID(request.NativeID)
	if err != nil {
		return nil, err
	}
	gvr, namespaced, err := c.Client.ResolveMapping(apiVersion, kind)
	if err != nil {
		return nil, err
	}
	err = c.resourceFor(gvr, namespaced, ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to delete %s/%s: %w", apiVersion, kind, err)
	}
	return &resource.DeleteResult{ProgressResult: &resource.ProgressResult{
		Operation: resource.OperationDelete, OperationStatus: resource.OperationStatusSuccess,
	}}, nil
}

// List is implemented in customlist.go.
