// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"
	"fmt"
)

// CustomLiveState renders the live state of an unstructured custom resource as
// JSON for Formae, with server-managed noise stripped and the formae identity
// field injected.
//
// Unlike LiveState[T], there is no typed apply-config round-trip available for
// an arbitrary CRD, so we operate directly on the unstructured object map:
//   - drop "status" (server-managed),
//   - drop the serverManagedMetaFields from metadata (uid, resourceVersion,
//     creationTimestamp, generation, deletionGracePeriodSeconds),
//   - inject "formaeId" = CustomResourceID(...) so the live identity matches
//     the pkl-computed desired identity ($.formaeId).
//
// obj is the .Object map of a *unstructured.Unstructured.
func CustomLiveState(obj map[string]interface{}) ([]byte, error) {
	apiVersion, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)

	delete(obj, "status")

	var name, namespace string
	if meta, ok := obj["metadata"].(map[string]interface{}); ok {
		for _, f := range serverManagedMetaFields {
			delete(meta, f)
		}
		name, _ = meta["name"].(string)
		namespace, _ = meta["namespace"].(string)
	}

	obj["formaeId"] = CustomResourceID(apiVersion, kind, namespace, name)

	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshal custom live state: %w", err)
	}
	return out, nil
}
