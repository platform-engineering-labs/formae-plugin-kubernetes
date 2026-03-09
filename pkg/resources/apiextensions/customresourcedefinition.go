// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package apiextensions

import (
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/generic"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const ResourceTypeCustomResourceDefinition = "K8S::ApiExtensions::CustomResourceDefinition"

func noinit() { //nolint:unused // registration disabled until CRD schema is ready
	generic.RegisterCRD(ResourceTypeCustomResourceDefinition, generic.CRDInfo{
		GVR: schema.GroupVersionResource{
			Group:    "apiextensions.k8s.io",
			Version:  "v1",
			Resource: "customresourcedefinitions",
		},
		Namespaced: false,
	}, []resource.Operation{
		resource.OperationCreate,
		resource.OperationRead,
		resource.OperationUpdate,
		resource.OperationDelete,
		resource.OperationList,
	})
}
