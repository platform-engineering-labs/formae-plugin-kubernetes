// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import "fmt"

// ResolveCreateNamespace returns the namespace from the apply config's
// metadata.namespace field. Namespaced resources MUST declare it explicitly
// in PKL; there is no implicit fallback to "default". This forces the user
// to either set metadata.namespace directly or resolve it from a Namespace
// resource declared in the same Forma.
func ResolveCreateNamespace(applyConfigNamespace *string, resourceType string) (string, error) {
	if applyConfigNamespace == nil || *applyConfigNamespace == "" {
		return "", fmt.Errorf("%s: metadata.namespace is required for namespaced resources", resourceType)
	}
	return *applyConfigNamespace, nil
}

// ResolveListNamespace returns the namespace passed by Formae's discovery via
// the parent K8S::Core::Namespace listParam (request.AdditionalProperties).
// Returns an error if the parameter is missing or empty — there is no implicit
// fallback. The PKL @ResourceHint must declare:
//
//	parent = "K8S::Core::Namespace"
//	listParam = new formae.ListProperty {
//	  parentProperty = "metadata.name"
//	  listParameter = "namespace"
//	}
func ResolveListNamespace(additionalProperties map[string]string, resourceType string) (string, error) {
	ns, ok := additionalProperties["namespace"]
	if !ok || ns == "" {
		return "", fmt.Errorf("%s: list requires 'namespace' parameter from parent Namespace listParam", resourceType)
	}
	return ns, nil
}
