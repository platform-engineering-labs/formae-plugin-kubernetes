//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"encoding/json"
	"testing"
)

// An explicitly empty collection in a forma's values must survive into the map
// handed to Helm.
//
// "Set this to []" and "do not mention this" are different instructions to a
// chart: the first overrides a default list, the second inherits it. Charts rely
// on the distinction — velero's `configuration.backupStorageLocation: []` is what
// stops it creating a BackupStorageLocation that its own CRD then rejects for
// having no credentials.
//
// Observed live: `helm install -f` with that override succeeded at revision 1 and
// `helm get values --revision 1` showed `configuration` intact. `formae extract`
// captured it correctly as `backupStorageLocation = new Listing {  }`, and
// `pkl eval --format json` on the same shape emits `"backupStorageLocation": []`.
// The upgrade formae then submitted arrived at Helm with `configuration` missing
// altogether — `helm get values --revision 2` shows only the two scalars — so the
// chart fell back to its default and the upgrade failed.
//
// This test pins the plugin's half of that path. If it passes, the drop happens
// before decodeProperties is called and belongs to formae core rather than here.
func TestDecodeProperties_KeepsExplicitlyEmptyCollections(t *testing.T) {
	raw := json.RawMessage(`{
		"metadata": {"name": "velero", "namespace": "ns"},
		"chart": "velero",
		"version": "12.1.0",
		"values": {
			"configuration": {
				"backupStorageLocation": [],
				"volumeSnapshotLocation": []
			},
			"credentials": {"useSecret": false},
			"snapshotsEnabled": false
		}
	}`)

	props, err := decodeProperties(raw)
	if err != nil {
		t.Fatalf("decodeProperties: %v", err)
	}

	config, ok := props.Values["configuration"].(map[string]any)
	if !ok {
		t.Fatalf("values.configuration decoded to %T, want a map — the empty lists took it with them",
			props.Values["configuration"])
	}
	for _, key := range []string{"backupStorageLocation", "volumeSnapshotLocation"} {
		list, present := config[key]
		if !present {
			t.Errorf("values.configuration.%s was dropped; the chart will fall back to its default", key)
			continue
		}
		items, ok := list.([]any)
		if !ok {
			t.Errorf("values.configuration.%s decoded to %T, want a slice", key, list)
			continue
		}
		if len(items) != 0 {
			t.Errorf("values.configuration.%s = %v, want an empty slice", key, items)
		}
	}

	// The scalars alongside them are the control: if these vanished too the
	// problem would be the whole values block, not empty collections specifically.
	if props.Values["snapshotsEnabled"] != false {
		t.Errorf("snapshotsEnabled = %v, want false", props.Values["snapshotsEnabled"])
	}
}

// Round-tripping the decoded values back to JSON must not drop them either —
// that is the shape the plugin hands to Helm.
func TestDecodeProperties_EmptyCollectionsSurviveReMarshal(t *testing.T) {
	raw := json.RawMessage(`{
		"metadata": {"name": "velero", "namespace": "ns"},
		"chart": "velero",
		"values": {"configuration": {"backupStorageLocation": []}}
	}`)

	props, err := decodeProperties(raw)
	if err != nil {
		t.Fatalf("decodeProperties: %v", err)
	}
	out, err := json.Marshal(props.Values)
	if err != nil {
		t.Fatalf("marshal values: %v", err)
	}
	if got := string(out); got != `{"configuration":{"backupStorageLocation":[]}}` {
		t.Errorf("values re-marshalled to %s, want the empty list preserved", got)
	}
}
