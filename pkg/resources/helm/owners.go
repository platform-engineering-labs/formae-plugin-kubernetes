// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"context"
	"fmt"
)

// OwnerRef is one ownerReferences entry, reduced to what the walk needs.
type OwnerRef struct {
	Kind string
	Name string
}

// OwnerLookup returns the ownerReferences of one object.
//
// Injected rather than called directly so the walk is testable without a
// cluster: the interesting cases are graph shapes (transitive chains, cycles,
// foreign owners), not apiserver mechanics.
type OwnerLookup func(ctx context.Context, kind, namespace, name string) ([]OwnerRef, error)

// maxOwnerDepth bounds the walk. A Pod reaches its Deployment in two hops
// (Pod -> ReplicaSet -> Deployment); operator-created objects reach their custom
// resource in one. Four is slack for a longer chain without letting a malformed
// graph run away.
const maxOwnerDepth = 4

// FilterControllerOwned removes objects whose ownerReferences chain reaches
// something a Helm release rendered.
//
// This is the complement to FilterHelmOwned, which only hides objects that are
// in a release manifest. Controllers create plenty that are not: the Pods and
// ReplicaSets behind a Deployment, and — the bigger share in practice — the
// Secrets, ConfigMaps, Services and StatefulSets an operator generates from a
// custom resource the chart rendered. Measured on kube-prometheus-stack, that is
// 13 of the 14 non-release rows discovery showed for the namespace; the one
// remainder is a hook-created Secret with no owner at all, which correctly stays.
//
// Why this cannot be a DiscoveryFilter: a static filter can only test whether
// ownerReferences exist, not whether the owner belongs to a release. Filtering
// every Secret that has an owner would hide Secrets belonging to a user's own
// operators. The decision needs the release inventory, so it lives here.
//
// Costs one lookup per candidate object plus one per distinct ancestor, and
// nothing at all when the cluster runs no Helm releases. A lookup failure keeps
// the object: hiding a real resource because one Get failed would be a silent
// loss, where an extra row is only noise — the same trade-off collapseHelmOwned
// makes for the inventory itself.
func FilterControllerOwned(
	ctx context.Context,
	inv *Inventory,
	resourceType string,
	nativeIDs []string,
	lookup OwnerLookup,
) []string {
	// No releases means nothing to attribute anything to. The common case on most
	// clusters, and it must not cost a single apiserver call.
	if inv == nil || inv.Len() == 0 || len(nativeIDs) == 0 || lookup == nil {
		return nativeIDs
	}
	kind := KindFromResourceType(resourceType)
	if kind == "" {
		return nativeIDs
	}

	// Answers memoised across objects, so five Pods behind one ReplicaSet resolve
	// their shared ancestors once. This caches the *result* per node, which is not
	// the same thing as the per-walk cycle guard below — conflating the two makes
	// the second object through a shared ancestor read as "not owned".
	memo := map[string]bool{}

	kept := make([]string, 0, len(nativeIDs))
	for _, id := range nativeIDs {
		ns, name, err := splitNativeID(id)
		if err != nil {
			kept = append(kept, id)
			continue
		}
		owned, err := reachesRelease(ctx, inv, lookup, memo, kind, ns, name)
		if err != nil || !owned {
			kept = append(kept, id)
		}
	}
	return kept
}

// reachesRelease walks ownerReferences upward looking for an object the
// inventory knows a release rendered.
//
// The whole chain is recorded as it is walked, so once the answer is known it is
// memoised for every node on it — a shared ancestor costs one lookup no matter
// how many objects hang off it.
func reachesRelease(
	ctx context.Context,
	inv *Inventory,
	lookup OwnerLookup,
	memo map[string]bool,
	kind, namespace, name string,
) (bool, error) {
	// Nodes visited on this walk only, to survive a malformed cycle.
	visiting := map[string]bool{}
	var chain []string

	remember := func(result bool) bool {
		for _, k := range chain {
			memo[k] = result
		}
		return result
	}

	curKind, curName := kind, name
	for depth := 0; depth < maxOwnerDepth; depth++ {
		key := curKind + "/" + namespace + "/" + curName
		if answer, ok := memo[key]; ok {
			return remember(answer), nil
		}
		if visiting[key] {
			return remember(false), nil // cycle
		}
		visiting[key] = true
		chain = append(chain, key)

		refs, err := lookup(ctx, curKind, namespace, curName)
		if err != nil {
			// Not memoised: a transient failure must not poison later objects.
			return false, fmt.Errorf("owner lookup for %s: %w", key, err)
		}
		if len(refs) == 0 {
			return remember(false), nil
		}
		// The controller reference is conventionally first, and an object with
		// several owners is rare enough that following the first is the honest
		// simple choice rather than a fan-out.
		curKind, curName = refs[0].Kind, refs[0].Name
		if curKind == "" || curName == "" {
			return remember(false), nil
		}
		// Cluster-scoped ancestors are indexed under an empty namespace too.
		if _, ok := inv.OwnedBy(curKind, namespace, curName); ok {
			return remember(true), nil
		}
		if _, ok := inv.OwnedBy(curKind, "", curName); ok {
			return remember(true), nil
		}
	}
	return remember(false), nil
}
