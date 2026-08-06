//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"context"
	"errors"
	"testing"
)

// fakeOwners is an in-memory ownerReferences graph keyed by kind/namespace/name.
type fakeOwners struct {
	refs  map[string][]OwnerRef
	calls int
	fail  map[string]error
}

func (f *fakeOwners) lookup(_ context.Context, kind, namespace, name string) ([]OwnerRef, error) {
	f.calls++
	key := kind + "/" + namespace + "/" + name
	if err, ok := f.fail[key]; ok {
		return nil, err
	}
	return f.refs[key], nil
}

// invWith builds an inventory holding just the given manifest objects.
func invWith(ns, owner string, objs ...[2]string) *Inventory {
	inv := newInventory()
	for _, o := range objs {
		kind, name := o[0], o[1]
		inv.objects[ObjectID{APIVersion: "v1", Kind: kind, Namespace: ns, Name: name}] = owner
		inv.byKind[kindRef{Kind: kind, Namespace: ns, Name: name}] = owner
		inv.byKind[kindRef{Kind: kind, Name: name}] = owner
	}
	return inv
}

// A Pod reaches its release through ReplicaSet then Deployment — three links, so
// a single-hop check would miss it.
func TestFilterControllerOwned_WalksTransitively(t *testing.T) {
	inv := invWith("ns", "ns/rel", [2]string{"Deployment", "web"})
	owners := &fakeOwners{refs: map[string][]OwnerRef{
		"Pod/ns/web-abc-1":      {{Kind: "ReplicaSet", Name: "web-abc"}},
		"ReplicaSet/ns/web-abc": {{Kind: "Deployment", Name: "web"}},
	}}

	got := FilterControllerOwned(context.Background(), inv, "K8S::Core::Pod",
		[]string{"ns/web-abc-1"}, owners.lookup)

	if len(got) != 0 {
		t.Errorf("expected the Pod to be hidden, got %v", got)
	}
}

// An operator-created Secret whose owner is a custom resource the chart rendered.
// This is the prometheus-operator shape: 13 such objects on kube-prometheus-stack.
func TestFilterControllerOwned_CustomResourceOwner(t *testing.T) {
	inv := invWith("ns", "ns/rel", [2]string{"Alertmanager", "am"})
	owners := &fakeOwners{refs: map[string][]OwnerRef{
		"Secret/ns/alertmanager-am-generated": {{Kind: "Alertmanager", Name: "am"}},
	}}

	got := FilterControllerOwned(context.Background(), inv, "K8S::Core::Secret",
		[]string{"ns/alertmanager-am-generated"}, owners.lookup)

	if len(got) != 0 {
		t.Errorf("expected the operator-created Secret to be hidden, got %v", got)
	}
}

// A Secret owned by somebody else's operator must survive: its chain never
// reaches anything a release rendered.
func TestFilterControllerOwned_KeepsForeignOwnedObject(t *testing.T) {
	inv := invWith("ns", "ns/rel", [2]string{"Deployment", "web"})
	owners := &fakeOwners{refs: map[string][]OwnerRef{
		"Secret/ns/their-secret": {{Kind: "TheirCRD", Name: "theirs"}},
	}}

	got := FilterControllerOwned(context.Background(), inv, "K8S::Core::Secret",
		[]string{"ns/their-secret"}, owners.lookup)

	if len(got) != 1 || got[0] != "ns/their-secret" {
		t.Errorf("a foreign-owned Secret must stay discoverable, got %v", got)
	}
}

// An object with no owner at all is somebody's own resource. This is the
// hook-created Secret case — nothing to attribute it to, so it must be reported.
func TestFilterControllerOwned_KeepsUnownedObject(t *testing.T) {
	inv := invWith("ns", "ns/rel", [2]string{"Deployment", "web"})
	owners := &fakeOwners{refs: map[string][]OwnerRef{}}

	got := FilterControllerOwned(context.Background(), inv, "K8S::Core::Secret",
		[]string{"ns/certgen-admission"}, owners.lookup)

	if len(got) != 1 {
		t.Errorf("an unowned Secret must stay discoverable, got %v", got)
	}
}

// A lookup failure must not hide the object. Dropping a real resource because one
// Get failed is a silent loss; showing an extra row is merely noise — the same
// trade-off collapseHelmOwned already makes.
func TestFilterControllerOwned_KeepsOnLookupError(t *testing.T) {
	inv := invWith("ns", "ns/rel", [2]string{"Deployment", "web"})
	owners := &fakeOwners{
		refs: map[string][]OwnerRef{},
		fail: map[string]error{"Secret/ns/unreadable": errors.New("forbidden")},
	}

	got := FilterControllerOwned(context.Background(), inv, "K8S::Core::Secret",
		[]string{"ns/unreadable"}, owners.lookup)

	if len(got) != 1 {
		t.Errorf("a Secret whose owner lookup failed must stay discoverable, got %v", got)
	}
}

// An ownerReferences cycle must not spin forever. Malformed, but cheap to survive.
func TestFilterControllerOwned_SurvivesOwnerCycle(t *testing.T) {
	inv := invWith("ns", "ns/rel", [2]string{"Deployment", "web"})
	owners := &fakeOwners{refs: map[string][]OwnerRef{
		"Secret/ns/a": {{Kind: "Secret", Name: "b"}},
		"Secret/ns/b": {{Kind: "Secret", Name: "a"}},
	}}

	got := FilterControllerOwned(context.Background(), inv, "K8S::Core::Secret",
		[]string{"ns/a"}, owners.lookup)

	if len(got) != 1 {
		t.Errorf("a cyclic chain reaches no release, so the object stays: got %v", got)
	}
}

// With no releases in the cluster there is nothing to attribute anything to, so
// the walk must not run at all — this is the common case on most clusters and it
// has to cost zero apiserver calls.
func TestFilterControllerOwned_NoReleasesCostsNothing(t *testing.T) {
	owners := &fakeOwners{refs: map[string][]OwnerRef{}}

	got := FilterControllerOwned(context.Background(), newInventory(), "K8S::Core::Secret",
		[]string{"ns/a", "ns/b"}, owners.lookup)

	if len(got) != 2 {
		t.Errorf("expected both kept, got %v", got)
	}
	if owners.calls != 0 {
		t.Errorf("expected no lookups with an empty inventory, made %d", owners.calls)
	}
}

// Objects sharing an ancestor must resolve it once, not once each. Five Pods
// behind one Deployment is the normal shape.
func TestFilterControllerOwned_SharedAncestorResolvedOnce(t *testing.T) {
	inv := invWith("ns", "ns/rel", [2]string{"Deployment", "web"})
	refs := map[string][]OwnerRef{"ReplicaSet/ns/web-abc": {{Kind: "Deployment", Name: "web"}}}
	var ids []string
	for _, n := range []string{"web-abc-1", "web-abc-2", "web-abc-3"} {
		refs["Pod/ns/"+n] = []OwnerRef{{Kind: "ReplicaSet", Name: "web-abc"}}
		ids = append(ids, "ns/"+n)
	}
	owners := &fakeOwners{refs: refs}

	got := FilterControllerOwned(context.Background(), inv, "K8S::Core::Pod", ids, owners.lookup)

	if len(got) != 0 {
		t.Errorf("expected all three Pods hidden, got %v", got)
	}
	// Three Pod lookups are unavoidable; the shared ReplicaSet must not be
	// looked up three times.
	if owners.calls > 4 {
		t.Errorf("expected at most 4 lookups (3 pods + 1 shared ReplicaSet), made %d", owners.calls)
	}
}
