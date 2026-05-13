// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultListPageSize is the page size used for K8S List operations.
// 500 matches kubectl's default and balances request count vs payload size.
const DefaultListPageSize = 500

// PagedList is the signature each provisioner adapts to in order to use
// ListAll. It returns the items from a single page, the continue token
// (empty when the listing is exhausted), and any error.
type PagedList[T any] func(ctx context.Context, opts metav1.ListOptions) (items []T, continueToken string, err error)

// ListAll loops over K8S List pagination and returns the concatenated items.
// Each page is requested with Limit=DefaultListPageSize and the previous
// page's Continue token. If a page request fails, ListAll returns whatever
// items it has accumulated so far together with the error so callers can
// distinguish full failure from partial-truncation cases if they choose to.
func ListAll[T any](ctx context.Context, list PagedList[T]) ([]T, error) {
	var (
		all           []T
		continueToken string
	)
	for {
		items, next, err := list(ctx, metav1.ListOptions{
			Limit:    DefaultListPageSize,
			Continue: continueToken,
		})
		if err != nil {
			return all, err
		}
		all = append(all, items...)
		if next == "" {
			return all, nil
		}
		continueToken = next
	}
}

// EachPage walks pages of a K8S List and invokes onPage for each one. It
// avoids the type-parameter ceremony required by ListAll: callers that only
// need to project list items into a flat []string or similar accumulator
// can stay non-generic. onPage receives the typed list pointer-as-any and
// the continue token; the caller is responsible for projecting list.Items.
//
// pager must invoke the typed client's List and return (listPtrAsAny, continueToken, error).
func EachPage(ctx context.Context,
	pager func(ctx context.Context, opts metav1.ListOptions) (continueToken string, err error),
) error {
	continueToken := ""
	for {
		next, err := pager(ctx, metav1.ListOptions{
			Limit:    DefaultListPageSize,
			Continue: continueToken,
		})
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		continueToken = next
	}
}
