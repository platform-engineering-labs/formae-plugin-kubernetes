// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package prov

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNamespacedNativeID_Valid(t *testing.T) {
	ns, name, err := ParseNamespacedNativeID("default/my-pod")
	require.NoError(t, err)
	assert.Equal(t, "default", ns)
	assert.Equal(t, "my-pod", name)
}

func TestParseNamespacedNativeID_Malformed(t *testing.T) {
	cases := []string{
		"",
		"/",
		"/foo",
		"foo/",
		"a/b/c",
		"justname",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, _, err := ParseNamespacedNativeID(c)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidNativeID)
		})
	}
}

func TestParseClusterNativeID_Valid(t *testing.T) {
	name, err := ParseClusterNativeID("my-cluster-role")
	require.NoError(t, err)
	assert.Equal(t, "my-cluster-role", name)
}

func TestParseClusterNativeID_Malformed(t *testing.T) {
	cases := []string{
		"",
		"/",
		"ns/name",
		"foo/",
		"/foo",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := ParseClusterNativeID(c)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidNativeID)
		})
	}
}

func TestParseNativeID_DeprecatedShim_StillAcceptsLegacyInputs(t *testing.T) {
	// The shim intentionally accepts malformed inputs to keep its old
	// behavior — callers must migrate to the strict variants.
	ns, name := ParseNativeID("default/foo")
	assert.Equal(t, "default", ns)
	assert.Equal(t, "foo", name)

	ns, name = ParseNativeID("standalone")
	assert.Empty(t, ns)
	assert.Equal(t, "standalone", name)
}

func TestErrInvalidNativeID_Sentinel(t *testing.T) {
	_, _, err := ParseNamespacedNativeID("")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidNativeID))
}
