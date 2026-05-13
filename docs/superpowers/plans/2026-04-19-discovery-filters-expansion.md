# Expand K8S Plugin Discovery Filters — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `Plugin.DiscoveryFilters()` in `k8s.go` with six additional filter categories so controller-owned children and system-owned cluster infrastructure are excluded from discovery. Add client-side `List()` filtering for Pods and ReplicaSets (the high-volume cases).

**Architecture:** All filters are declarative `plugin.MatchFilter` entries evaluated by the formae agent at discovery time, keyed off `metadata.ownerReferences` (for controller children) or exact property matches (for system-owned resources). For Pods and ReplicaSets, the plugin's `List()` method additionally skips entries with owner references before returning native IDs — the same pattern used by `pkg/resources/rbac/clusterrole.go`.

**Tech Stack:** Go, client-go, `github.com/platform-engineering-labs/formae/pkg/plugin`, `github.com/stretchr/testify`

**Spec:** `docs/superpowers/specs/2026-04-19-discovery-filters-expansion.md`
**RFC:** TBD

---

## File Map

### New files

- `k8s_test.go` — Unit tests for `DiscoveryFilters()`: structural assertions that each expected filter is present with the right `ResourceTypes` and `Conditions` shape. Uses `//go:build unit` tag.
- `pkg/resources/apps/replicaset_integration_test.go` — Integration test verifying `ReplicaSet.List()` skips owner-referenced items (follows `pkg/resources/rbac/rbac_integration_test.go` pattern).
- `pkg/resources/core/pod_integration_test.go` — Integration test verifying `Pod.List()` skips owner-referenced items.

### Modified files

- `k8s.go` — Add six new `plugin.MatchFilter` entries in `DiscoveryFilters()` (lines 74-124).
- `pkg/resources/apps/replicaset.go` — Add owner-reference skip to `List()` (mirrors clusterrole.go:204-210).
- `pkg/resources/core/pod.go` — Add owner-reference skip to `List()`.
- `conformance_test.go` — Add a discovery conformance case: create a Deployment, run discovery, assert child ReplicaSet and Pods are not surfaced as unmanaged.

---

## Task 1: Test scaffolding and CronJob-owned Job filter

Creates `k8s_test.go` as the unit-test home for `DiscoveryFilters()` and adds the first new filter. Establishes the assertion helper pattern reused by later tasks.

**Files:**

- Create: `k8s_test.go`
- Modify: `k8s.go` (add a filter entry in `DiscoveryFilters()`, after the existing `system:*` filters near line 122)

- [ ] **Step 1: Write the failing test**

Write to `k8s_test.go`:

```go
//go:build unit

package main

import (
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findFilter returns the first MatchFilter that applies to resourceType and
// whose first condition's PropertyPath contains pathSubstring. The substring
// match lets tests assert intent (e.g. "a filter on ownerReferences for Pods
// exists") without hard-coding exact strings.
func findFilter(t *testing.T, filters []plugin.MatchFilter, resourceType, pathSubstring string) plugin.MatchFilter {
	t.Helper()
	for _, f := range filters {
		if len(f.Conditions) == 0 {
			continue
		}
		hasType := false
		for _, rt := range f.ResourceTypes {
			if rt == resourceType {
				hasType = true
				break
			}
		}
		if !hasType {
			continue
		}
		if pathSubstring == "" || containsString(f.Conditions[0].PropertyPath, pathSubstring) {
			return f
		}
	}
	t.Fatalf("no filter found for resourceType=%q containing path substring %q", resourceType, pathSubstring)
	return plugin.MatchFilter{}
}

func containsString(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestDiscoveryFilters_CronJobOwnedJobs(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Batch::Job", "ownerReferences")
	require.Len(t, f.Conditions, 1, "filter should have exactly one condition")
	assert.Contains(t, f.Conditions[0].PropertyPath, "CronJob",
		"condition should match owner references of kind CronJob")
}
```

Note: the small string helpers (`containsString`, `indexOf`) avoid importing `strings` inside the helper while keeping it obvious — they are used only from `findFilter`. If preferred, replace with `strings.Contains` and `strings.Index`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=unit -run TestDiscoveryFilters_CronJobOwnedJobs ./...`

Expected: FAIL with "no filter found for resourceType=\"K8S::Batch::Job\" containing path substring \"ownerReferences\"" (the filter hasn't been added yet).

- [ ] **Step 3: Add the filter**

In `k8s.go`, inside `DiscoveryFilters()`, append after the existing `system:*` ClusterRoleBinding filter (near the closing `}` of the return slice, around line 122):

```go
		// Exclude Jobs created by a CronJob on schedule. Standalone Jobs
		// (no ownerReferences) are still discovered.
		{
			ResourceTypes: []string{"K8S::Batch::Job"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: `$.metadata.ownerReferences[?@.kind == "CronJob"]`},
			},
		},
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -tags=unit -run TestDiscoveryFilters_CronJobOwnedJobs ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add k8s.go k8s_test.go
git commit -m "feat(discovery): filter CronJob-owned Jobs"
```

---

## Task 2: ReplicaSet owner-reference filter (declarative + List-side)

Adds the declarative filter for any ReplicaSet with an ownerReference AND the client-side `List()` filter in the ReplicaSet provisioner. Both are covered: unit test for the declarative filter, integration test for `List()`.

**Files:**

- Modify: `k8s.go`
- Modify: `k8s_test.go`
- Modify: `pkg/resources/apps/replicaset.go`
- Create: `pkg/resources/apps/replicaset_integration_test.go`

- [ ] **Step 1: Write the failing unit test for the declarative filter**

Append to `k8s_test.go`:

```go
func TestDiscoveryFilters_OwnedReplicaSets(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Apps::ReplicaSet", "ownerReferences")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "ownerReferences")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=unit -run TestDiscoveryFilters_OwnedReplicaSets ./...`

Expected: FAIL — no filter yet for `K8S::Apps::ReplicaSet`.

- [ ] **Step 3: Add the declarative filter**

In `k8s.go`, in `DiscoveryFilters()`, add after the Job filter from Task 1:

```go
		// Exclude ReplicaSets owned by a Deployment. Standalone ReplicaSets
		// (rare, but valid) are still discovered.
		{
			ResourceTypes: []string{"K8S::Apps::ReplicaSet"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.ownerReferences[0]"},
			},
		},
```

- [ ] **Step 4: Run the unit test to verify it passes**

Run: `go test -tags=unit -run TestDiscoveryFilters_OwnedReplicaSets ./...`

Expected: PASS.

- [ ] **Step 5: Write the failing integration test for List-side filtering**

First, confirm existing integration pattern. Read `pkg/resources/rbac/rbac_integration_test.go` and `pkg/resources/testutil` to see how `SetupEnv` and test resources are created.

Create `pkg/resources/apps/replicaset_integration_test.go`:

```go
//go:build integration

package apps_test

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/apps"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplicaSetList_SkipsOwnedReplicaSets(t *testing.T) {
	env := testutil.SetupEnv(t)
	ctx := context.Background()

	// Create a standalone ReplicaSet (no owner reference) — should appear in List.
	standalone := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "standalone-rs",
			Namespace: env.Namespace,
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: ptrInt32(0),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "standalone"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "standalone"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "pause"}}},
			},
		},
	}
	_, err := env.Client.AppsV1().ReplicaSets(env.Namespace).Create(ctx, standalone, metav1.CreateOptions{})
	require.NoError(t, err)

	// Create a Deployment; the apiserver/controller creates a child ReplicaSet with ownerRef.
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owner-dep",
			Namespace: env.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptrInt32(0),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "owned"}},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &intstr.IntOrString{Type: intstr.Int, IntVal: 0},
					MaxSurge:       &intstr.IntOrString{Type: intstr.Int, IntVal: 1},
				}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "owned"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "pause"}}},
			},
		},
	}
	_, err = env.Client.AppsV1().Deployments(env.Namespace).Create(ctx, deployment, metav1.CreateOptions{})
	require.NoError(t, err)

	// Wait until the deployment controller materializes a ReplicaSet.
	require.Eventually(t, func() bool {
		list, err := env.Client.AppsV1().ReplicaSets(env.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false
		}
		for _, rs := range list.Items {
			if len(rs.OwnerReferences) > 0 {
				return true
			}
		}
		return false
	}, 30*time.Second, 250*time.Millisecond, "Deployment did not produce an owned ReplicaSet")

	// Now call the plugin's List.
	rsProv := &apps.ReplicaSet{Client: env.Client, Config: env.Config}
	result, err := rsProv.List(ctx, &resource.ListRequest{AdditionalProperties: map[string]string{"namespace": env.Namespace}})
	require.NoError(t, err)

	// Only the standalone ReplicaSet should appear — owned one must be filtered out.
	assert.Contains(t, result.NativeIDs, env.Namespace+"/standalone-rs")
	for _, id := range result.NativeIDs {
		assert.NotContains(t, id, "owner-dep-", "owned ReplicaSets must be filtered from List")
	}
}

func ptrInt32(v int32) *int32 { return &v }
```

Reference: `pkg/resources/testutil/testutil.go` defines `SetupEnv(t)` returning a `*TestEnv{Client, Config, Namespace, ...}` backed by the `orbstack` kubeconfig context. The plugin's provisioners are plain structs (no `New*` constructor) — instantiate with `&apps.ReplicaSet{Client: env.Client, Config: env.Config}`. `require.Eventually` replaces a missing `testutil.WaitUntil` helper.

- [ ] **Step 6: Run the integration test to verify it fails**

Run: `go test -v -tags=integration -run TestReplicaSetList_SkipsOwnedReplicaSets ./pkg/resources/apps/...`

Expected: FAIL — the owned ReplicaSet currently appears in `result.NativeIDs`, triggering the `assert.NotContains` failure.

- [ ] **Step 7: Add List-side filtering to ReplicaSet provisioner**

Read `pkg/resources/apps/replicaset.go` to locate the `List` method (by convention around line 205 — mirrors other provisioners). Then modify it:

```go
// inside ReplicaSet.List, at the loop that builds nativeIDs:
nativeIDs := make([]string, 0, len(result.Items))
for _, rs := range result.Items {
	// Skip ReplicaSets owned by a controller (typically a Deployment).
	// Filtering at List level prevents formae from processing controller-
	// created ReplicaSets through the changeset pipeline during discovery.
	if len(rs.OwnerReferences) > 0 {
		continue
	}
	nativeIDs = append(nativeIDs, prov.NativeID(rs.Namespace, rs.Name))
}
```

- [ ] **Step 8: Run the integration test to verify it passes**

Run: `go test -v -tags=integration -run TestReplicaSetList_SkipsOwnedReplicaSets ./pkg/resources/apps/...`

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add k8s.go k8s_test.go pkg/resources/apps/replicaset.go pkg/resources/apps/replicaset_integration_test.go
git commit -m "feat(discovery): filter controller-owned ReplicaSets"
```

---

## Task 3: Pod owner-reference filter (declarative + List-side)

Same structure as Task 2, for Pods. Separate task because Pods are the highest-volume resource and merit an independent commit.

**Files:**

- Modify: `k8s.go`
- Modify: `k8s_test.go`
- Modify: `pkg/resources/core/pod.go`
- Create: `pkg/resources/core/pod_integration_test.go`

- [ ] **Step 1: Write the failing unit test**

Append to `k8s_test.go`:

```go
func TestDiscoveryFilters_OwnedPods(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Core::Pod", "ownerReferences")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "ownerReferences")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=unit -run TestDiscoveryFilters_OwnedPods ./...`

Expected: FAIL.

- [ ] **Step 3: Add the declarative filter**

In `k8s.go`, in `DiscoveryFilters()`, add after the ReplicaSet filter from Task 2:

```go
		// Exclude Pods created by a controller (Deployment/ReplicaSet, Job,
		// StatefulSet, DaemonSet). Standalone Pods are still discovered.
		{
			ResourceTypes: []string{"K8S::Core::Pod"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.ownerReferences[0]"},
			},
		},
```

- [ ] **Step 4: Run the unit test to verify it passes**

Run: `go test -tags=unit -run TestDiscoveryFilters_OwnedPods ./...`

Expected: PASS.

- [ ] **Step 5: Write the failing integration test**

Create `pkg/resources/core/pod_integration_test.go`:

```go
//go:build integration

package core_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/core"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPodList_SkipsOwnedPods(t *testing.T) {
	env := testutil.SetupEnv(t)
	ctx := context.Background()

	// Standalone Pod — should appear in List.
	standalone := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "standalone-pod",
			Namespace: env.Namespace,
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "pause"}}},
	}
	_, err := env.Client.CoreV1().Pods(env.Namespace).Create(ctx, standalone, metav1.CreateOptions{})
	require.NoError(t, err)

	// Pod with a synthetic owner reference — should NOT appear in List.
	owned := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owned-pod",
			Namespace: env.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "ReplicaSet",
				Name:       "fake-rs",
				UID:        types.UID("00000000-0000-0000-0000-000000000000"),
			}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "pause"}}},
	}
	_, err = env.Client.CoreV1().Pods(env.Namespace).Create(ctx, owned, metav1.CreateOptions{})
	require.NoError(t, err)

	podProv := &core.Pod{Client: env.Client, Config: env.Config}
	result, err := podProv.List(ctx, &resource.ListRequest{AdditionalProperties: map[string]string{"namespace": env.Namespace}})
	require.NoError(t, err)

	assert.Contains(t, result.NativeIDs, env.Namespace+"/standalone-pod")
	assert.NotContains(t, result.NativeIDs, env.Namespace+"/owned-pod",
		"owned Pods must be filtered from List")
}
```

Note: a synthetic owner reference on a manually-created Pod is accepted by the apiserver — the controller doesn't need to exist for the field to be set. This keeps the test hermetic (no Deployment controller racing).

The provisioner is a plain struct (see `pkg/resources/core/pod.go`) — no constructor exists; instantiate directly as `&core.Pod{Client: env.Client, Config: env.Config}`.

- [ ] **Step 6: Run the integration test to verify it fails**

Run: `go test -v -tags=integration -run TestPodList_SkipsOwnedPods ./pkg/resources/core/...`

Expected: FAIL — the owned Pod currently appears in `result.NativeIDs`.

- [ ] **Step 7: Add List-side filtering to Pod provisioner**

In `pkg/resources/core/pod.go`, in `List`:

```go
nativeIDs := make([]string, 0, len(result.Items))
for _, pod := range result.Items {
	// Skip Pods owned by a controller (ReplicaSet, Job, DaemonSet,
	// StatefulSet). Filtering at List level prevents formae from
	// processing controller-managed Pods through the changeset pipeline
	// during discovery.
	if len(pod.OwnerReferences) > 0 {
		continue
	}
	nativeIDs = append(nativeIDs, prov.NativeID(pod.Namespace, pod.Name))
}
```

- [ ] **Step 8: Run the integration test to verify it passes**

Run: `go test -v -tags=integration -run TestPodList_SkipsOwnedPods ./pkg/resources/core/...`

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add k8s.go k8s_test.go pkg/resources/core/pod.go pkg/resources/core/pod_integration_test.go
git commit -m "feat(discovery): filter controller-owned Pods"
```

---

## Task 4: Endpoints owner-reference filter (declarative only)

Endpoints are 1:1 with Services and lower-volume than Pods; the declarative filter alone suffices (no List-side change).

**Files:**

- Modify: `k8s.go`
- Modify: `k8s_test.go`

- [ ] **Step 1: Write the failing test**

Append to `k8s_test.go`:

```go
func TestDiscoveryFilters_OwnedEndpoints(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Core::Endpoints", "ownerReferences")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "ownerReferences")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=unit -run TestDiscoveryFilters_OwnedEndpoints ./...`

Expected: FAIL.

- [ ] **Step 3: Add the filter**

In `k8s.go`, in `DiscoveryFilters()`:

```go
		// Exclude Endpoints objects — K8S maintains one per Service with
		// the Service as owner.
		{
			ResourceTypes: []string{"K8S::Core::Endpoints"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.ownerReferences[0]"},
			},
		},
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -tags=unit -run TestDiscoveryFilters_OwnedEndpoints ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add k8s.go k8s_test.go
git commit -m "feat(discovery): filter Service-owned Endpoints"
```

---

## Task 5: Service-account-token Secret filter

Filter `kubernetes.io/service-account-token` Secrets. The `.type` field is the authoritative K8S signal.

**Files:**

- Modify: `k8s.go`
- Modify: `k8s_test.go`

- [ ] **Step 1: Write the failing test**

Append to `k8s_test.go`:

```go
func TestDiscoveryFilters_ServiceAccountTokenSecrets(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Core::Secret", "type")
	require.Len(t, f.Conditions, 1)
	assert.Equal(t, "$.type", f.Conditions[0].PropertyPath)
	assert.Equal(t, "kubernetes.io/service-account-token", f.Conditions[0].PropertyValue)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=unit -run TestDiscoveryFilters_ServiceAccountTokenSecrets ./...`

Expected: FAIL.

- [ ] **Step 3: Add the filter**

In `k8s.go`, in `DiscoveryFilters()`:

```go
		// Exclude auto-generated ServiceAccount token Secrets.
		{
			ResourceTypes: []string{"K8S::Core::Secret"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.type", PropertyValue: "kubernetes.io/service-account-token"},
			},
		},
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -tags=unit -run TestDiscoveryFilters_ServiceAccountTokenSecrets ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add k8s.go k8s_test.go
git commit -m "feat(discovery): filter service-account-token Secrets"
```

---

## Task 6: kube-system Lease filter

All Leases in `kube-system` are control-plane leader-election artifacts.

**Files:**

- Modify: `k8s.go`
- Modify: `k8s_test.go`

- [ ] **Step 1: Write the failing test**

Append to `k8s_test.go`:

```go
func TestDiscoveryFilters_KubeSystemLeases(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Coordination::Lease", "namespace")
	require.Len(t, f.Conditions, 1)
	assert.Equal(t, "$.metadata.namespace", f.Conditions[0].PropertyPath)
	assert.Equal(t, "kube-system", f.Conditions[0].PropertyValue)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=unit -run TestDiscoveryFilters_KubeSystemLeases ./...`

Expected: FAIL.

- [ ] **Step 3: Add the filter**

In `k8s.go`, in `DiscoveryFilters()`:

```go
		// Exclude Leases in kube-system — all are control-plane leader
		// election artifacts (kube-controller-manager, kube-scheduler,
		// cloud-controller-manager, node leases, etc.).
		{
			ResourceTypes: []string{"K8S::Coordination::Lease"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.namespace", PropertyValue: "kube-system"},
			},
		},
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -tags=unit -run TestDiscoveryFilters_KubeSystemLeases ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add k8s.go k8s_test.go
git commit -m "feat(discovery): filter kube-system Leases"
```

---

## Task 7: System FlowSchema and PriorityLevelConfiguration filters

These ship with the apiserver and are universally named `system-*` (plus `exempt` and `global-default` for FlowSchema).

**Files:**

- Modify: `k8s.go`
- Modify: `k8s_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `k8s_test.go`:

```go
func TestDiscoveryFilters_SystemFlowSchemas(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Flowcontrol::FlowSchema", "system-")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "system-")
}

func TestDiscoveryFilters_SystemPriorityLevelConfigurations(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Flowcontrol::PriorityLevelConfiguration", "system-")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "system-")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -tags=unit -run "TestDiscoveryFilters_System(FlowSchemas|PriorityLevelConfigurations)" ./...`

Expected: FAIL (both).

- [ ] **Step 3: Add the filters**

In `k8s.go`, in `DiscoveryFilters()`. Reuse the `search()` pattern the existing `system:*` RBAC filters use (for consistency):

```go
		// Exclude system-* FlowSchemas (ship with the apiserver).
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^system-')]`},
			},
		},
		// FlowSchema also includes 'exempt' and 'global-default' — match them explicitly.
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "exempt"},
			},
		},
		{
			ResourceTypes: []string{"K8S::Flowcontrol::FlowSchema"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: "$.metadata.name", PropertyValue: "global-default"},
			},
		},
		// Exclude system-* PriorityLevelConfigurations.
		{
			ResourceTypes: []string{"K8S::Flowcontrol::PriorityLevelConfiguration"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^system-')]`},
			},
		},
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -tags=unit -run "TestDiscoveryFilters_System(FlowSchemas|PriorityLevelConfigurations)" ./...`

Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
git add k8s.go k8s_test.go
git commit -m "feat(discovery): filter system FlowSchemas and PriorityLevelConfigurations"
```

---

## Task 8: System PriorityClass filter

K8S ships two `system-*` PriorityClasses; the `search()` prefix match covers both and future additions.

**Files:**

- Modify: `k8s.go`
- Modify: `k8s_test.go`

- [ ] **Step 1: Write the failing test**

Append to `k8s_test.go`:

```go
func TestDiscoveryFilters_SystemPriorityClasses(t *testing.T) {
	p := &Plugin{}
	filters := p.DiscoveryFilters()

	f := findFilter(t, filters, "K8S::Scheduling::PriorityClass", "system-")
	require.Len(t, f.Conditions, 1)
	assert.Contains(t, f.Conditions[0].PropertyPath, "system-")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=unit -run TestDiscoveryFilters_SystemPriorityClasses ./...`

Expected: FAIL.

- [ ] **Step 3: Add the filter**

In `k8s.go`, in `DiscoveryFilters()`:

```go
		// Exclude system-* PriorityClasses (system-cluster-critical,
		// system-node-critical — ship with the apiserver).
		{
			ResourceTypes: []string{"K8S::Scheduling::PriorityClass"},
			Conditions: []plugin.FilterCondition{
				{PropertyPath: `$.metadata[?search(@, '^system-')]`},
			},
		},
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -tags=unit -run TestDiscoveryFilters_SystemPriorityClasses ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add k8s.go k8s_test.go
git commit -m "feat(discovery): filter system PriorityClasses"
```

---

## Task 9: End-to-end discovery conformance test

Validates that the declarative filters are honored by the agent when running real discovery — the agent is what evaluates `MatchFilter`, so end-to-end coverage is necessary to catch JSONPath syntax errors the unit tests cannot.

**Files:**

- Modify: `conformance_test.go` (or `testdata/` per existing conformance patterns)

- [ ] **Step 1: Inspect the existing conformance discovery test pattern**

Read `conformance_test.go` and the relevant `testdata/` to understand how a discovery conformance case is structured. Identify:
- How resources are created outside formae
- How `formae discover` is invoked and parsed
- How assertions on the discovered set are written

- [ ] **Step 2: Write the failing test**

Add a conformance case that:

1. Uses `kubectl`/client-go to `kubectl apply -f` a plain Deployment (pure K8S, not via formae) in a test namespace — ensures a ReplicaSet and Pod are created by the controllers.
2. Wait until the ReplicaSet and at least one Pod are materialized.
3. Run `formae discover` (or whichever CLI invocation the existing conformance tests use).
4. Parse the discovered resources and assert:
   - The Deployment IS surfaced (it has no owner).
   - No `K8S::Apps::ReplicaSet` belonging to this Deployment is surfaced.
   - No `K8S::Core::Pod` belonging to this Deployment is surfaced.
5. Clean up by `kubectl delete deployment`.

Follow the exact test idiom the existing discovery conformance tests use. Name the test something like `TestDiscovery_SkipsControllerOwnedChildren`.

- [ ] **Step 3: Run the conformance test to verify it fails**

Run: `make conformance-test TEST=controller-owned-children`

(Or whatever filter the conformance framework uses — check the Makefile target and adapt.)

Expected: If the new DiscoveryFilters are already in place from Tasks 2-3, this may PASS immediately. That is acceptable — the test documents the end-to-end contract. If it fails, the failure is a bug in the declarative filter JSONPath (e.g., agent evaluator doesn't parse the syntax as expected) and must be fixed before proceeding.

- [ ] **Step 4: Fix any JSONPath issues**

If the conformance test fails with "owned ReplicaSet/Pod appeared in discovery," the agent's JSONPath evaluator is interpreting the path differently than expected. The most likely adjustments:

- `$.metadata.ownerReferences[0]` → `$.metadata.ownerReferences[*]` (any element rather than specifically the first).
- `$.metadata.ownerReferences[?@.kind == "CronJob"]` → try single-quoted form `$.metadata.ownerReferences[?(@.kind=='CronJob')]` if the agent uses classic JSONPath syntax.

Verify by running the agent locally with verbose discovery logs, or by checking how the AWS plugin's filter expressions are structured (which we know work).

- [ ] **Step 5: Run the conformance test to verify it passes**

Run: `make conformance-test TEST=controller-owned-children`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add conformance_test.go testdata/
git commit -m "test(discovery): conformance coverage for controller-owned child filtering"
```

---

## Final verification

Before finishing, run the full test suites:

- [ ] `make test-unit` — all unit tests pass.
- [ ] `make test-integration` — all integration tests pass (needs a K8S cluster; OrbStack context).
- [ ] `make conformance-test-discovery` — all discovery conformance tests pass.
- [ ] `make lint` — no new lint warnings.
