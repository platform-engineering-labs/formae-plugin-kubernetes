# Handoff: Provider-Agnostic K8S Auth

**Date:** 2026-04-09
**Previous session:** Design + planning complete, implementation not started
**Branch:** main (no implementation branch created yet)

## What We Did

1. **Analyzed CI conformance test failures** — flaky OOB-delete timeouts and
   destroy failures across nightly runs. Different resource types each run.
   Someone else has since fixed these.

2. **Reviewed the EKS + K8S cross-cloud example** — understood the two-stage
   (`stage1-eks.pkl` + `stage2-webapp.pkl`) and single-forma
   (`test-resolvable.pkl`) approaches. The resolvable approach works for EKS
   but auth is hardcoded to AWS.

3. **Researched all cloud plugin K8S offerings** — surveyed managed K8S
   resources across AWS (EKS), Azure (AKS), GCP (GKE), OVH, and OCI (OKE).
   Catalogued endpoint/CA cert availability and auth mechanisms for each.

4. **Designed provider-agnostic auth system** — brainstormed approaches,
   selected auth union type on target config with `AuthProvider` interface.
   Key decisions:
   - `AuthProvider.ConfigureTransport(*rest.Config)` instead of narrow
     `TokenProvider` returning strings
   - `CachedTokenSource` with singleflight for concurrent token refresh
   - `WrapTransport` on `rest.Config` for per-request token injection
   - Formae core nested config hint support as blocking prerequisite
   - Removes the existing 50-second client cache TTL workaround in `k8s.go`

5. **Wrote design spec** — comprehensive spec with all design choices documented.

6. **Published RFC-0029** — draft PR in the rfcs repo with full motivation,
   alternatives considered, and testing sections.

7. **Ran Codex adversarial review** — incorporated three findings: broadened
   interface from `TokenProvider` to `AuthProvider`, added `CachedTokenSource`
   with explicit concurrency contract, added blocking prerequisite ordering
   for formae core change.

8. **Reviewed colleague's competing RFC** — their "shared KubeConfig resource"
   proposal doesn't address token refresh or resource lifecycle semantics.
   Draft review feedback written.

9. **Wrote implementation plan** — 10 tasks with TDD steps, exact file paths,
   complete code, and test commands.

## Key Documents

| Document | Path |
|----------|------|
| **Design spec** | `docs/superpowers/specs/2026-04-07-provider-agnostic-k8s-auth-design.md` |
| **Implementation plan** | `docs/superpowers/plans/2026-04-09-provider-agnostic-k8s-auth.md` |
| **RFC-0029 (draft PR)** | https://github.com/platform-engineering-labs/rfcs/pull/9 |
| **Engineering note** | `~/dev/personal/engineering-notes/formae/2026-04-07-k8s-has-load-balancer-config-relocation.md` |
| **CLAUDE.md** | Created for this repo at project root |

## What Needs Doing

**Execute the implementation plan** using subagent-driven development. The plan
has 10 tasks:

1. AuthProvider interface + TokenTransport + CachedTokenSource
2. CachedTokenSource tests (caching, expiry, singleflight)
3. EKS AuthProvider (move from `pkg/config/auth.go`)
4. GKE, AKS, OVH, OCI AuthProviders
5. PKL schema — new Config + auth union types
6. Config struct rewrite + auth dispatching
7. Transport client + plugin simplification (remove client cache)
8. Update all PKL test/example files
9. Add cloud SDK dependencies
10. Integration verification (lint, tests, conformance)

**Blocking dependency (separate repo):** Formae core needs nested config hint
support in `ClassifyConfigChange` before the K8S plugin schema change can merge.
This lives in `platform-engineering-labs/formae` at
`internal/metastructure/target_update/classify_config_change.go`. The K8S plugin
work can be developed in parallel but should not merge first.

**Cloud plugin resolvables:** A colleague is working on adding the missing
resolvable properties (endpoint, CA cert) to Azure, OVH, and OCI plugins. Not
blocking — `KubeconfigAuth` is the fallback. AWS resolvables are already in
place for testing.

## Open Issue

**GitHub issue #21** — `writeOnly + createOnly` fields trigger phantom resource
replacement in patch mode. Affects AWS EKS Cluster. Fix is in the main formae
repo. Assigned to Jeroen.

## Context

- Plugin is pre-release, no backwards compatibility concerns
- Integration tests use OrbStack context (hardcoded in `pkg/resources/testutil/testutil.go`)
- Conformance tests run on kind clusters in CI
- The existing `k8s.go` has a 50-second client cache TTL as a workaround for
  EKS token expiry — the new design eliminates this entirely via `WrapTransport`
