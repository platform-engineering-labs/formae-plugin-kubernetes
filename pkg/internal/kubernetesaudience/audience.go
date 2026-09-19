// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Package kubernetesaudience validates direct Kubernetes OIDC audiences.
package kubernetesaudience

import (
	"encoding/hex"
	"strings"
)

const prefix = "urn:formae:kubernetes:"

// Valid reports whether audience is the exact reserved prefix followed by a
// canonical lowercase, non-nil RFC 4122 UUID with version 1 through 5.
func Valid(audience string) bool {
	id, ok := strings.CutPrefix(audience, prefix)
	if !ok || len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	raw, err := hex.DecodeString(id[0:8] + id[9:13] + id[14:18] + id[19:23] + id[24:36])
	if err != nil || len(raw) != 16 {
		return false
	}
	canonical := hex.EncodeToString(raw[0:4]) + "-" + hex.EncodeToString(raw[4:6]) + "-" +
		hex.EncodeToString(raw[6:8]) + "-" + hex.EncodeToString(raw[8:10]) + "-" + hex.EncodeToString(raw[10:16])
	if id != canonical {
		return false
	}
	allZero, allMax := true, true
	for _, b := range raw {
		allZero = allZero && b == 0
		allMax = allMax && b == 0xff
	}
	version := raw[6] >> 4
	return !allZero && !allMax && raw[8]&0xc0 == 0x80 && version >= 1 && version <= 5
}
