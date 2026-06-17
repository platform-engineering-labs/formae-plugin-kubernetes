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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// ResourceTypeCustom is the catch-all type for custom-resource instances and any
// K8s kind without a typed provisioner.
const ResourceTypeCustom = "K8S::Custom::Resource"

// ResourceTypeCRD is a distinct type for CustomResourceDefinition objects. It
// uses the same generic provisioner (the provisioner reads apiVersion/kind from
// the manifest, not the formae type), but a separate type lets a CRD and an
// instance of it coexist in one forma — they no longer collide on the catch-all
// type. That is what makes a CRD + its CR deployable in a single formae apply
// (and testable as a multi-resource conformance fixture without bootstrapping
// the CRD out-of-band).
const ResourceTypeCRD = "K8S::Apiextensions::CustomResourceDefinition"

func init() {
	ops := []resource.Operation{
		resource.OperationCreate,
		resource.OperationRead,
		resource.OperationUpdate,
		resource.OperationDelete,
		resource.OperationList,
	}
	factory := func(client *transport.Client, cfg *config.Config) prov.Provisioner {
		return &CustomResource{Client: client, Config: cfg}
	}
	registry.Register(ResourceTypeCustom, ops, factory)
	registry.Register(ResourceTypeCRD, ops, factory)
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

// parseManifest normalizes incoming property JSON, strips the formae-only
// formaeId field, and returns the unstructured object.
func (c *CustomResource) parseManifest(raw []byte) (*unstructured.Unstructured, error) {
	normalized, err := prov.NormalizeMetadata(raw)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(normalized, &m); err != nil {
		return nil, fmt.Errorf("unmarshal custom manifest: %w", err)
	}
	// formaeId is a formae-only identity field, never sent to the apiserver.
	delete(m, "formaeId")

	obj := &unstructured.Unstructured{Object: m}
	if obj.GetAPIVersion() == "" || obj.GetKind() == "" {
		return nil, fmt.Errorf("custom manifest missing apiVersion or kind")
	}
	return obj, nil
}

// resourceFor returns the dynamic client handle scoped correctly: namespaced
// kinds are bound to a namespace, cluster-scoped kinds are not.
func (c *CustomResource) resourceFor(gvr schema.GroupVersionResource, namespaced bool, namespace string) dynamic.ResourceInterface {
	if namespaced {
		return c.Client.Dynamic.Resource(gvr).Namespace(namespace)
	}
	return c.Client.Dynamic.Resource(gvr)
}

// resolveAndApply resolves the object's GVR and applies it via SSA, retrying
// within crdEstablishTimeout. On each retryable failure it resets the RESTMapper
// so a stale cache entry is re-discovered. This makes a custom resource whose
// CRD is being created concurrently (same forma apply) converge once the CRD is
// established — without an explicit dependency edge — and survive destroy/
// recreate cycles where the CRD is briefly terminating or its kind unserved.
//
// Retryable conditions:
//   - no REST mapping yet (CRD not registered) — meta.IsNoMatchError
//   - kind not served yet (CRD not established) — apiserver 404 / IsNotFound
//   - CRD still terminating ("object is being deleted") — IsConflict
//   - transient apiserver errors — timeout / internal / throttling / already-exists
func (c *CustomResource) resolveAndApply(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	apiVersion, kind := obj.GetAPIVersion(), obj.GetKind()
	deadline := time.Now().Add(crdEstablishTimeout)
	for {
		gvr, namespaced, err := c.Client.ResolveMapping(apiVersion, kind)
		if err == nil {
			ns := obj.GetNamespace()
			if namespaced && ns == "" {
				ns = "default"
				obj.SetNamespace(ns)
			}
			var result *unstructured.Unstructured
			result, err = c.resourceFor(gvr, namespaced, ns).Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{
				FieldManager: prov.FieldManager,
				Force:        true,
			})
			if err == nil {
				return result, nil
			}
		}
		if !isEstablishRetryable(err) || time.Now().After(deadline) {
			return nil, err
		}
		// Force fresh discovery: the kind may have just been (re)created, so a
		// cached "exists / doesn't exist" entry must not stick.
		c.Client.ResetMapper()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(crdEstablishBackoff):
		}
	}
}

// crdGVR is the GroupVersionResource for CustomResourceDefinition objects,
// used by discovery (customlist.go) to enumerate installed CRDs.
var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// isEstablishRetryable reports whether an apply/resolve error is the kind that
// clears once a CRD finishes (re)establishing.
func isEstablishRetryable(err error) bool {
	return meta.IsNoMatchError(err) ||
		apierrors.IsNotFound(err) ||
		apierrors.IsConflict(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsInternalError(err) ||
		apierrors.IsTooManyRequests(err)
}

func (c *CustomResource) apply(ctx context.Context, raw []byte, op resource.Operation) (*resource.ProgressResult, error) {
	obj, err := c.parseManifest(raw)
	if err != nil {
		return nil, err
	}
	result, err := c.resolveAndApply(ctx, obj)
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
