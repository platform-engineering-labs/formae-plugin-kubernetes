// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestResolveMappingWith_NamespacedAndCluster(t *testing.T) {
	gvNamespaced := schema.GroupVersion{Group: "cert-manager.io", Version: "v1"}
	gvCluster := schema.GroupVersion{Group: "apiextensions.k8s.io", Version: "v1"}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gvNamespaced, gvCluster})
	mapper.Add(gvNamespaced.WithKind("Certificate"), meta.RESTScopeNamespace)
	mapper.Add(gvCluster.WithKind("CustomResourceDefinition"), meta.RESTScopeRoot)

	gvr, namespaced, err := resolveMappingWith(mapper, "cert-manager.io/v1", "Certificate")
	if err != nil {
		t.Fatalf("namespaced resolve: %v", err)
	}
	if gvr.Resource != "certificates" || !namespaced {
		t.Fatalf("got %v namespaced=%v", gvr, namespaced)
	}

	gvr, namespaced, err = resolveMappingWith(mapper, "apiextensions.k8s.io/v1", "CustomResourceDefinition")
	if err != nil {
		t.Fatalf("cluster resolve: %v", err)
	}
	if gvr.Resource != "customresourcedefinitions" || namespaced {
		t.Fatalf("got %v namespaced=%v", gvr, namespaced)
	}
}

func TestResolveMappingWith_Unknown(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper(nil)
	if _, _, err := resolveMappingWith(mapper, "nope.io/v1", "Ghost"); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}
