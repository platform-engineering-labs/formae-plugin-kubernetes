// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"
	"fmt"
)

// stringMapKeys are JSON object keys whose values are, by Kubernetes contract,
// `map[string]string` — i.e. label/annotation maps. Formae core's property
// serializer treats `.` as a path separator and, on state-DB round-trip,
// expands a flat key like `app.kubernetes.io/name` into a nested object
// `app: { kubernetes: { "io/name": ... } }`. Typed Go client-side apply
// configurations declare these fields as `map[string]string` and reject the
// nested-object shape with:
//
//	json: cannot unmarshal object into Go struct field
//	ObjectMetaApplyConfiguration.metadata.labels of type string
//
// We pre-walk incoming property JSON and collapse any such nested objects
// back into flat dotted-key string maps before unmarshaling. Already-flat
// maps are a no-op.
// stringMapKeys lists JSON object keys whose value is always a flat
// map[string]string in the K8s API contract. The `selector` key is handled
// separately below because it is overloaded: Service.spec.selector and
// ReplicationController.spec.selector are flat maps, but Deployment /
// ReplicaSet / Job / StatefulSet / DaemonSet `spec.selector` is a
// LabelSelector wrapper object containing `matchLabels` (flat) and
// `matchExpressions` (list).
var stringMapKeys = map[string]bool{
	"labels":       true,
	"annotations":  true,
	"matchLabels":  true,
	"nodeSelector": true,
}

// isLabelSelectorShape reports whether m looks like a LabelSelector
// (`matchLabels` and/or `matchExpressions`) rather than a flat selector map.
func isLabelSelectorShape(m map[string]interface{}) bool {
	if _, ok := m["matchLabels"]; ok {
		return true
	}
	if _, ok := m["matchExpressions"]; ok {
		return true
	}
	return false
}

// NormalizeMetadata walks a JSON document and rewrites every label/annotation/
// selector map whose values were dot-expanded into nested objects back to a
// flat map of dotted-key strings. It is safe to call on JSON that is already
// well-formed: untouched.
//
// This is a defensive shim around a Formae core bug. The right long-term fix
// is to stop dot-expanding inside string-typed K8s metadata maps in the core
// property model; until that lands, every provisioner unmarshal site must
// go through NormalizeMetadata or UnmarshalApplyConfig (which calls it).
func NormalizeMetadata(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("normalize: parse json: %w", err)
	}
	walkAndFlatten(doc)
	return json.Marshal(doc)
}

// UnmarshalApplyConfig is a drop-in replacement for json.Unmarshal at every
// provisioner entry point: it normalizes string-map keys before unmarshaling
// so dot-expanded labels/annotations from Formae core state survive the
// round-trip into typed apply configurations.
func UnmarshalApplyConfig(raw []byte, dst interface{}) error {
	normalized, err := NormalizeMetadata(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, dst)
}

// walkAndFlatten recursively traverses the parsed JSON document. When it hits
// a key listed in stringMapKeys whose value is a JSON object, it rebuilds
// that object so every value is a primitive string and every key is the
// fully dotted path that originally lived there.
func walkAndFlatten(v interface{}) {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, child := range val {
			obj, isObj := child.(map[string]interface{})
			switch {
			case stringMapKeys[k] && isObj && hasNestedObjectValue(obj):
				val[k] = collapseStringMap(obj, "")
			case k == "selector" && isObj && !isLabelSelectorShape(obj) && hasNestedObjectValue(obj):
				// Flat selector (Service / ReplicationController). LabelSelector
				// variants are left intact so the recursive walk can still hit
				// their nested matchLabels.
				val[k] = collapseStringMap(obj, "")
			default:
				walkAndFlatten(child)
			}
		}
	case []interface{}:
		for _, child := range val {
			walkAndFlatten(child)
		}
	}
}

// hasNestedObjectValue reports whether any value in m is a JSON object
// (i.e. the map was dot-expanded). Maps whose values are all primitives
// don't need to be touched.
func hasNestedObjectValue(m map[string]interface{}) bool {
	for _, v := range m {
		if _, ok := v.(map[string]interface{}); ok {
			return true
		}
	}
	return false
}

// collapseStringMap rebuilds a dot-expanded nested object into a flat
// map[string]string-shaped result, joining nested keys with "." to
// reconstruct the original dotted key (e.g. `app.kubernetes.io/name`).
// Non-string leaves are coerced via fmt.Sprintf("%v", ...) since K8s
// metadata maps are always string-valued; the apiserver will reject any
// payload that ends up otherwise.
func collapseStringMap(m map[string]interface{}, prefix string) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range m {
		fullKey := k
		if prefix != "" {
			fullKey = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]interface{}:
			for sk, sv := range collapseStringMap(val, fullKey) {
				out[sk] = sv
			}
		case string:
			out[fullKey] = val
		case nil:
			out[fullKey] = ""
		default:
			out[fullKey] = fmt.Sprintf("%v", val)
		}
	}
	return out
}
