// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package custom

import (
	"context"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestList_NoAllowlist_ReturnsNothing(t *testing.T) {
	c := &CustomResource{Config: &config.Config{}} // empty CustomResourceGroups
	res, err := c.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(res.NativeIDs) != 0 {
		t.Fatalf("expected no native IDs, got %d", len(res.NativeIDs))
	}
}
