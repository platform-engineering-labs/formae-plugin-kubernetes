// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package core

import (
	"encoding/json"
	"testing"
)

func TestEnrichDecodedData_AddsDecodedMap(t *testing.T) {
	// client-go returns Secret.Data already base64-decoded (map[string][]byte),
	// so the raw bytes are the plaintext value.
	props := []byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"s","namespace":"ns"},"type":"Opaque"}`)
	data := map[string][]byte{
		"username": []byte("admin"),
		"password": []byte("s3cr3t"),
	}

	enriched, err := enrichDecodedData(props, data)
	if err != nil {
		t.Fatalf("enrichDecodedData: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(enriched, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	decoded, ok := got["decodedData"].(map[string]any)
	if !ok {
		t.Fatalf("decodedData missing or wrong type: %#v", got["decodedData"])
	}
	if decoded["username"] != "admin" {
		t.Errorf("decodedData.username = %q, want admin", decoded["username"])
	}
	if decoded["password"] != "s3cr3t" {
		t.Errorf("decodedData.password = %q, want s3cr3t", decoded["password"])
	}

	// Existing fields are preserved.
	if got["kind"] != "Secret" {
		t.Errorf("kind = %q, want Secret", got["kind"])
	}
}

func TestEnrichDecodedData_EmptyDataLeavesPropertiesUnchanged(t *testing.T) {
	props := []byte(`{"apiVersion":"v1","kind":"Secret"}`)

	for name, data := range map[string]map[string][]byte{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			enriched, err := enrichDecodedData(props, data)
			if err != nil {
				t.Fatalf("enrichDecodedData: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(enriched, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, present := got["decodedData"]; present {
				t.Errorf("decodedData should be absent for %s data", name)
			}
		})
	}
}

// A value that happens to contain bytes that look like base64 must NOT be
// decoded a second time: client-go already decoded the wire form, so the raw
// bytes pass through verbatim.
func TestEnrichDecodedData_DoesNotDoubleDecode(t *testing.T) {
	props := []byte(`{"kind":"Secret"}`)
	// "YWRtaW4=" is the base64 of "admin", but here it is the literal value.
	data := map[string][]byte{"token": []byte("YWRtaW4=")}

	enriched, err := enrichDecodedData(props, data)
	if err != nil {
		t.Fatalf("enrichDecodedData: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(enriched, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	decoded := got["decodedData"].(map[string]any)
	if decoded["token"] != "YWRtaW4=" {
		t.Errorf("decodedData.token = %q, want the verbatim value YWRtaW4=", decoded["token"])
	}
}
