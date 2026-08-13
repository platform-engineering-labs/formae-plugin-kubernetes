// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// ObjectID identifies one Kubernetes object inside a Helm release manifest.
// Group/version are kept as the raw apiVersion string because that is what the
// manifest carries and what the plugin's resource types map back to.
type ObjectID struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

// Inventory is the set of objects Helm renders for every release in a cluster.
//
// This is the ownership signal, in preference to the
// `app.kubernetes.io/managed-by: Helm` label. The label is a chart *convention*
// — chart authors skip it, and the `meta.helm.sh/release-*` annotations do not
// propagate into pod templates — whereas the manifest is what Helm actually
// applied. It is also cheaper here: formae's List returns "namespace/name"
// strings, so a set-membership test needs no extra apiserver reads, where a
// label check would need a Get per object.
type Inventory struct {
	objects map[ObjectID]string // object -> "namespace/releaseName"
	byKind  map[kindRef]string  // kind+ns+name -> "namespace/releaseName"
}

// kindNamespace is special-cased by both filters: a Namespace is a discovery
// parent, so hiding one hides everything inside it.
const kindNamespace = "Namespace"

// kindRef identifies an object without its apiVersion.
//
// The filter matches on Kind alone because formae's ListResult carries only
// "namespace/name" strings — the apiVersion is never in hand at filter time, and
// reconstructing it would need a resource-type-to-group table that rots every
// time an API graduates a version. Two distinct kinds sharing a name across
// groups would collide here; none of the plugin's registered types do.
type kindRef struct {
	Kind      string
	Namespace string
	Name      string
}

// OwnedBy returns the release that rendered kind/namespace/name, if any.
func (i *Inventory) OwnedBy(kind, namespace, name string) (string, bool) {
	if i == nil {
		return "", false
	}
	owner, ok := i.byKind[kindRef{Kind: kind, Namespace: namespace, Name: name}]
	return owner, ok
}

// Has reports whether apiVersion/kind/namespace/name was rendered by a release.
func (i *Inventory) Has(apiVersion, kind, namespace, name string) bool {
	if i == nil {
		return false
	}
	_, ok := i.objects[ObjectID{APIVersion: apiVersion, Kind: kind, Namespace: namespace, Name: name}]
	return ok
}

// KindFromResourceType extracts the Kubernetes kind from a formae resource type.
// "K8S::Core::ConfigMap" -> "ConfigMap". Returns "" when the type is not the
// expected three-segment shape.
func KindFromResourceType(resourceType string) string {
	parts := strings.Split(resourceType, "::")
	if len(parts) != 3 {
		return ""
	}
	return parts[2]
}

// FilterHelmOwned removes native IDs whose object was rendered by a Helm
// release, so a chart's Deployments, Services and hook Jobs do not surface as
// unmanaged resources alongside the K8S::Helm::Release that owns them.
//
// Only manifest-direct objects are removed. Pods, ReplicaSets and EndpointSlices
// created by controllers downstream of a chart's Deployment are not in any
// manifest, and hiding them would mean resolving ownerReferences with one Get
// per object. They still surface; that cost is measured before building the
// walker.
func FilterHelmOwned(inv *Inventory, resourceType string, nativeIDs []string) []string {
	kind := KindFromResourceType(resourceType)
	if kind == "" || inv == nil || len(nativeIDs) == 0 {
		return nativeIDs
	}
	if kind == kindNamespace {
		// Never hide a Namespace, even one a chart rendered. Every namespaced
		// type declares parent = K8S::Core::Namespace and discovery walks
		// children per *discovered* namespace, so dropping a Namespace removes
		// everything inside it from discovery — objects Helm never touched
		// included, and any K8S::Helm::Release installed there with them.
		//
		// Measured on a chart that templates its own namespace: a hand-made
		// ConfigMap inside it vanished from discovery entirely. One extra
		// unmanaged Namespace row is by far the cheaper mistake.
		return nativeIDs
	}
	kept := make([]string, 0, len(nativeIDs))
	for _, id := range nativeIDs {
		ns, name, err := splitNativeID(id)
		if err != nil {
			// Unparseable IDs are passed through rather than dropped: silently
			// swallowing one would remove a real resource from discovery.
			kept = append(kept, id)
			continue
		}
		if _, owned := inv.OwnedBy(kind, ns, name); owned {
			continue
		}
		kept = append(kept, id)
	}
	return kept
}

// splitNativeID accepts both "namespace/name" and cluster-scoped "name".
func splitNativeID(id string) (namespace, name string, err error) {
	switch parts := strings.Split(id, "/"); len(parts) {
	case 1:
		return "", parts[0], nil
	case 2:
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("unexpected native id %q", id)
	}
}

// InventoryFor exposes the cached release inventory to the plugin's List router.
func InventoryFor(ctx context.Context, cfg *config.Config) (*Inventory, error) {
	return cachedInventory(ctx, cfg)
}

// Len reports the number of indexed objects. Used by tests and logging.
func (i *Inventory) Len() int {
	if i == nil {
		return 0
	}
	return len(i.objects)
}

// buildInventory lists every release in every namespace and indexes the objects
// each one renders, including hook objects.
//
// Hooks are indexed too. A hook Job is Helm's to schedule and reap; surfacing it
// as an unmanaged formae resource is the exact leak that made the
// template-and-decompose approach wrong.
func buildInventory(ctx context.Context, cfg *config.Config) (*Inventory, error) {
	// Namespace "" plus AllNamespaces: the storage driver needs a namespace to
	// construct its client, but the list itself spans the cluster.
	conf, err := newActionConfig(cfg, "")
	if err != nil {
		return nil, err
	}

	list := action.NewList(conf)
	list.All = true
	list.AllNamespaces = true
	// Include pending and failed releases. Their objects exist in the cluster
	// even though the release never reached "deployed", so leaving them out
	// would surface a half-installed chart's objects as unmanaged.
	list.SetStateMask()

	releases, err := list.Run()
	if err != nil {
		return nil, fmt.Errorf("list helm releases: %w", err)
	}

	inv := newInventory()
	for _, rel := range releases {
		owner := rel.Namespace + "/" + rel.Name
		indexManifest(inv, rel.Manifest, rel.Namespace, owner)
		for _, h := range rel.Hooks {
			indexManifest(inv, h.Manifest, rel.Namespace, owner)
		}
	}
	return inv, nil
}

// indexManifest parses a multi-document YAML manifest and adds each object.
//
// Documents that fail to parse are skipped rather than failing the whole
// inventory: a single odd document must not cause every release's objects to be
// treated as unmanaged, which would flood discovery.
func indexManifest(inv *Inventory, manifest, defaultNamespace, owner string) {
	for _, doc := range splitYAMLDocs(manifest) {
		var head struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil {
			continue
		}
		if head.Kind == "" || head.Metadata.Name == "" {
			continue
		}
		ns := head.Metadata.Namespace
		if ns == "" {
			// Helm renders most objects without an explicit namespace and lets
			// the release namespace apply.
			ns = defaultNamespace
		}
		inv.objects[ObjectID{
			APIVersion: head.APIVersion,
			Kind:       head.Kind,
			Namespace:  ns,
			Name:       head.Metadata.Name,
		}] = owner
		inv.byKind[kindRef{Kind: head.Kind, Namespace: ns, Name: head.Metadata.Name}] = owner
		// Cluster-scoped objects (CRDs, ClusterRoles, …) reach the filter with an
		// empty namespace, but Helm renders them with the release namespace
		// defaulted in above. Index both so either form matches.
		if ns != "" {
			inv.byKind[kindRef{Kind: head.Kind, Name: head.Metadata.Name}] = owner
		}
	}
}

func newInventory() *Inventory {
	return &Inventory{
		objects: make(map[ObjectID]string),
		byKind:  make(map[kindRef]string),
	}
}

// splitYAMLDocs splits on the YAML document separator. Only "---" at the start
// of a line counts, so a "---" inside a string value is not a split point.
func splitYAMLDocs(manifest string) []string {
	var docs []string
	var cur strings.Builder
	for _, line := range strings.Split(manifest, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "---" || strings.HasPrefix(trimmed, "--- ") {
			if strings.TrimSpace(cur.String()) != "" {
				docs = append(docs, cur.String())
			}
			cur.Reset()
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	if strings.TrimSpace(cur.String()) != "" {
		docs = append(docs, cur.String())
	}
	return docs
}

// inventoryTTL bounds how stale the discovery filter may be.
//
// Short because the failure mode is user-visible: run `helm upgrade` by hand and
// a longer window leaves objects hidden that no release renders any more (or
// vice versa). Our own writes invalidate immediately via invalidateInventory, so
// the TTL only has to cover out-of-band Helm CLI use.
const inventoryTTL = 30 * time.Second

type inventoryEntry struct {
	inv     *Inventory
	fetched time.Time
}

var (
	invMu    sync.Mutex
	invCache = map[string]inventoryEntry{}
)

// cachedInventory returns the release inventory for a target, rebuilding it when
// the cached copy has aged out.
//
// Caching is not an optimization here, it is load-bearing: formae calls List
// once per resource type, so ~200 calls per discovery pass. Each rebuild
// gunzips every release Secret in the cluster, and a chart like flux renders
// hundreds of KB. Uncached this would be two orders of magnitude more work per
// pass.
func cachedInventory(ctx context.Context, cfg *config.Config) (*Inventory, error) {
	key, err := cfg.CacheKey()
	if err != nil {
		return nil, err
	}

	invMu.Lock()
	entry, ok := invCache[key]
	invMu.Unlock()
	if ok && time.Since(entry.fetched) < inventoryTTL {
		return entry.inv, nil
	}

	inv, err := buildInventory(ctx, cfg)
	if err != nil {
		return nil, err
	}

	invMu.Lock()
	invCache[key] = inventoryEntry{inv: inv, fetched: time.Now()}
	invMu.Unlock()
	return inv, nil
}

// invalidateInventory drops the cached inventory for a target. Called after this
// plugin installs, upgrades or uninstalls a release so the next discovery pass
// reflects the change without waiting out the TTL.
func invalidateInventory(cfg *config.Config) {
	key, err := cfg.CacheKey()
	if err != nil {
		return
	}
	invMu.Lock()
	delete(invCache, key)
	invMu.Unlock()
}

// releaseIsPending reports whether a release is mid-operation according to Helm.
// Helm treats these states as a pessimistic lock — both Install and Upgrade
// refuse to touch a release in one of them.
func releaseIsPending(rel *release.Release) bool {
	return rel != nil && rel.Info != nil && rel.Info.Status.IsPending()
}
