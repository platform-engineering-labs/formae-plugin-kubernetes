# Drop hasLoadBalancer Config from K8S Target

**Date:** 2026-04-19
**Status:** Draft
**Authors:** Jeroen Soeters

## Problem

The K8S plugin's target config carries a `hasLoadBalancer` boolean (previously
`waitForLoadBalancer`) that tells the Service and Ingress provisioners whether
to wait for a load balancer address before reporting `Success`. When true (the
default), the provisioner returns `InProgress` while `status.loadBalancer.
ingress` is empty; the ResourceUpdater polls until the cluster populates it.

Three things are wrong with this.

**It is misplaced.** The flag sits on the target (cluster-wide) Config, but is
consulted by exactly two of 35+ resource types: `K8S::Core::Service` (only
when `spec.type == LoadBalancer`) and `K8S::Networking::Ingress`. For every
other resource type on the target, it is dead weight.

**It is a footgun on local clusters.** The default is `true`, so out of the
box, applying a LoadBalancer Service or any Ingress against a cluster without
a cloud LB controller (OrbStack, kind, minikube) hangs forever. The fix
is to set `hasLoadBalancer: false` on the target — but the user has to know
the flag exists, guess it applies to their cluster, and learn what the right
value is.

**The behavior it gates has no user-visible value.** This is the insight that
makes the flag unnecessary entirely, not just misplaced. The wait-for-LB
logic blocks `Success` until a cluster controller finishes assigning an
address. But:

- The LB address lives in `status.loadBalancer.ingress[].ip`, and
  `pkg/resources/prov/livestate.go:109` strips the entire `status` field
  before the properties are persisted. formae never records the address.

- The CLI renderer (`internal/cli/renderer/renderer.go:518-533`) only renders
  `ProgressResult.StatusMessage` for `InProgress`, `Failed`, and `Rejected`
  states. On `Success` the message is not rendered. The
  `"load balancer IP: 203.0.113.42"` message the provisioner builds on
  completion is therefore discarded; the user sees a green checkmark with no
  address.

So the "wait" does not persist anything, does not display anything, and does
not gate any dependent resource (nothing in formae reads a Service or Ingress'
LB address as a resolvable). It is a block-and-discard.

## Goals

- Remove `hasLoadBalancer` from the K8S target Config
- One well-defined behavior for Service and Ingress across cloud and local
  clusters, no config knob
- No regression in persisted resource state or user-visible output

## Non-Goals

- Surfacing the LB address to the user (separate concern; can be added later
  via `formae inventory` behavior or a dedicated output if there is user demand)
- Per-resource-type plugin configuration (tracked in the approved
  `resource-plugin-config` design)

## Design

### The signal hunt

Before arriving at "never wait," we looked for a signal that would let the
plugin decide *when* to wait, so the flag could be eliminated while keeping
the wait behavior where it is correct. Every candidate fell over.

**Auth type.** The plugin already knows the cluster's auth type (EKS, GKE,
AKS, OVH, OCI, Kubeconfig). For the five cloud auth types, "cluster has an
LB controller" is a certainty. For `Kubeconfig` it is unknown — and crucially,
it is common for users to run `aws eks update-kubeconfig` (or the GKE/AKS
equivalent) and then point formae at the kubeconfig context. Those users'
clusters *do* have LB controllers, but the plugin cannot tell from the auth
type alone.

**Service spec (`spec.type == LoadBalancer`).** For Service, this is a
reliable declaration from the user: "I expect an LB to be provisioned." So
for Service the plugin has a property-level signal it can trust. Ingress has
no equivalent — Ingress does not declare "I expect an LB" in its spec; that
depends on the ingress controller installed on the cluster. Using the Service
signal for Service only and falling back to auth type for Ingress brings the
Kubeconfig ambiguity back in through the Ingress door.

This is where the investigation pivoted. If the plugin cannot cleanly decide
when to wait, maybe waiting is not worth doing at all. That led to looking at
what the wait actually accomplishes in terms of formae state and user-visible
output — and as the Problem section describes, the answer is "nothing."

### The proposal

Both Service and Ingress provisioners return `OperationStatusSuccess`
immediately after a successful Server-Side Apply. No LB status check, no
config flag, no per-cluster heuristic.

`hasLoadBalancer` is removed from target Config (Go struct and PKL schema).

### Implications

**Persisted state is unchanged.** `LiveState` already strips `status`, so no
property has ever included the LB address. The datastore view of a Service
or Ingress is identical before and after this change.

**User-visible output is unchanged on success.** The renderer never showed
the completion `statusMessage`. Users see the same green checkmark.

**Apply time drops for LB resources on cloud clusters.** Where cloud users
previously waited 30-90s for the cloud controller to provision the LB before
`formae apply` returned, apply now returns as soon as SSA completes. This is
a behavior change, but in the direction users likely want — apply is faster
and reflects what formae actually guarantees (the resource spec is applied),
not what a downstream controller will eventually do (assign an address).

**Apply succeeds on local clusters.** The infinite-hang failure mode is gone.

**Out-of-band sync is unaffected.** The sync loop reads live state, strips
`status`, and compares against persisted properties. No LB-related drift is
possible because the address is not in persisted properties on either side.

### Getting the LB address

The change hides a fact that the provisioner currently discovers: the LB
address. Since nothing in the CLI path today surfaces that address to users,
hiding it changes nothing user-facing. Users who want the address retrieve it
the same way they would on any K8S cluster:

```
kubectl get svc <name> -n <ns>
kubectl get ingress <name> -n <ns>
```

This is the standard workflow for inspecting K8S `status` fields (LB address,
pod readiness, rollout state, etc.), and formae intentionally does not try to
replace it.

## Alternatives Considered

**Auto-detection with timeout.** Always try to wait for an LB address; after
N seconds give up and return `Success` with a "timed out" message. Rejected:
the timeout framing implies failure when the resource was created correctly,
and it makes local-cluster users wait N seconds for something that will never
arrive.

**Move the flag to a per-resource PKL property.** Add `waitForLoadBalancer:
Boolean?` to Service and Ingress schemas. Rejected: introduces boilerplate on
every LB Service and Ingress the user writes, for a behavior that produces no
user-visible output on completion anyway.

**Flip the default to `false`.** Keep the flag as opt-in for cloud users.
Rejected: keeps a misplaced target-level flag, and the behavior it controls
has no value.

**Infer from auth type.** Cloud auth → wait, Kubeconfig → don't wait.
Rejected: users with `Kubeconfig` auth pointed at a cloud cluster are common,
making the inference unreliable. More importantly, since the waited-for
information is never displayed or persisted, inference quality does not
matter — the operation is the same either way.

**Use Service spec for Service, something else for Ingress.** Split the
signal by resource type. Rejected: brings Kubeconfig ambiguity back in for
Ingress, and the spec-level signal for Service is correct but points at
behavior that still has no user-visible value.

## Testing

- Unit tests for `operationStatus` on both provisioners updated to assert
  `OperationStatusSuccess` regardless of LB state.
- Integration tests for LoadBalancer Service and Ingress on OrbStack must
  complete without hanging — this is the regression being fixed.
- Conformance tests that previously depended on the cloud controller having
  assigned an address by the time Apply returned need to either issue a
  separate `Read` after a delay, or drop the address assertion. Audit and
  update any such cases.
- Remove any test fixtures or helpers that set `HasLoadBalancer` on target
  config.

## References

- Engineering note:
  `engineering-notes/formae-plugin-k8s/2026-04-07-k8s-has-load-balancer-config-relocation.md`
- Engineering note:
  `engineering-notes/formae-plugin-k8s/2026-04-11-k8s-loadbalancer-infinite-wait.md`
- `pkg/config/config.go` — `HasLoadBalancerController()`
- `schema/pkl/k8s.pkl` — `hasLoadBalancer` on `Config`
- `pkg/resources/core/service.go:234` — Service provisioner LB check
- `pkg/resources/networking/ingress.go:237` — Ingress provisioner LB check
- `pkg/resources/prov/livestate.go:109` — `status` stripping
- `formae/internal/cli/renderer/renderer.go:518-533` — renderer only shows
  `StatusMessage` for non-success states
