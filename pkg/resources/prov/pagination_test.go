// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package prov

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestListAll_ThreePages(t *testing.T) {
	// 3 pages of 500 = 1500 items
	pages := [][]int{}
	for p := 0; p < 3; p++ {
		page := make([]int, 0, 500)
		for i := 0; i < 500; i++ {
			page = append(page, p*500+i)
		}
		pages = append(pages, page)
	}

	idx := 0
	items, err := ListAll(context.Background(), func(_ context.Context, opts metav1.ListOptions) ([]int, string, error) {
		page := pages[idx]
		idx++
		// Last page returns empty continue token
		next := ""
		if idx < len(pages) {
			next = fmt.Sprintf("token-%d", idx)
		}
		return page, next, nil
	})
	require.NoError(t, err)
	assert.Len(t, items, 1500)
	assert.Equal(t, 0, items[0])
	assert.Equal(t, 1499, items[1499])
	assert.Equal(t, 3, idx, "should have called list exactly 3 times")
}

func TestListAll_EmptyResult(t *testing.T) {
	items, err := ListAll(context.Background(), func(_ context.Context, _ metav1.ListOptions) ([]int, string, error) {
		return nil, "", nil
	})
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestListAll_MidLoopErrorReturnsPartial(t *testing.T) {
	wantErr := errors.New("boom")
	idx := 0
	items, err := ListAll(context.Background(), func(_ context.Context, _ metav1.ListOptions) ([]int, string, error) {
		idx++
		if idx == 1 {
			return []int{1, 2, 3}, "next", nil
		}
		return nil, "", wantErr
	})
	require.Error(t, err)
	assert.Equal(t, wantErr, err)
	assert.Equal(t, []int{1, 2, 3}, items, "should return items already accumulated")
}

func TestEachPage_StopsOnEmptyContinue(t *testing.T) {
	calls := 0
	err := EachPage(context.Background(), func(_ context.Context, opts metav1.ListOptions) (string, error) {
		calls++
		return "", nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestEachPage_ContinuesUntilExhausted(t *testing.T) {
	calls := 0
	err := EachPage(context.Background(), func(_ context.Context, opts metav1.ListOptions) (string, error) {
		calls++
		if calls < 3 {
			assert.Equal(t, int64(DefaultListPageSize), opts.Limit)
			return fmt.Sprintf("tok-%d", calls), nil
		}
		return "", nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
}

func TestEachPage_PropagatesError(t *testing.T) {
	wantErr := errors.New("paged-out")
	err := EachPage(context.Background(), func(_ context.Context, _ metav1.ListOptions) (string, error) {
		return "", wantErr
	})
	require.Error(t, err)
	assert.Equal(t, wantErr, err)
}
