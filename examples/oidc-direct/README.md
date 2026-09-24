# Installation OIDC: DIRECT

The complete prerequisites are also embedded in `main.pkl`, because MCP returns Pkl files only.

Installation OIDC prerequisites (these comments are part of the MCP example).
This source example evaluates against the checkout schema via PklProject:
  ["k8s"] = import("../../schema/pkl/PklProject")
The MCP bundler rewrites that local self import to the resolved package. In a
standalone project, use ["k8s"] { uri = "package://hub.platform.engineering/plugins/k8s/schema/pkl/k8s/k8s@0.1.13" }.
Installation OIDC requires formae 0.90.2 or newer; this example pins
["formae"] { uri = "package://hub.platform.engineering/plugins/pkl/schema/pkl/formae/formae@0.90.2" }.
Coordinates below are fictitious public metadata. Replace them, including CA.

This is an ordinary top-level K8S target: no parent cloud target is needed.
Existing cloud provisioning targets stay unchanged. Reuse their public role,
app or WIF coordinates only if that identity also has separate cluster grants.
Obtain the installation's EXACT PERSISTED subject from onboarding; never derive
it from a display name, tenant name or guessed installation identifier.
Federated issuer: https://oidc.cloud.formae.ai. Pair a compatible credential
broker with namespace K8S and allow the audience described below. Core, SDK
and broker must support bounded mint requests; a missing/old broker fails closed.
The agent must reach the cluster and canonical provider token endpoints. Issuer
discovery/JWKS must be reachable and trusted by the token verifier. Cluster CA
and issuer HTTPS trust are separate. Tokens refresh in memory during live plugin
callbacks; no token, client secret, kubeconfig credential or assertion belongs
in target config or forma state. Explicit OIDC never falls back to ambient auth.

Bootstrap in stages: (1) create/discover cluster, (2) establish federation and
cluster access grants as administrator and wait for propagation, (3) apply a NEW
K8S target and workload. Endpoint/CA resolvables order metadata availability,
NOT separate grants. Do not assume one-apply bootstrap or reverse teardown.
Remove workloads while access still exists; only then remove grants/cluster.
Auth is createOnly: changing auth on a populated target can REPLACE the target
and recreate its managed resources ACROSS STACKS. Do not migrate in place.
See https://docs.formae.io/documentation/concepts/target and /resolvable.

Helm gate: broker-backed Helm needs trusted binding/timing metadata from the
compatible core and a sufficient timeoutSeconds (default timing requires a
refresh window W=130s; use 300s or more). No standing mint actor runs between
callbacks. Create/Update/Delete and recovering Status additionally GET the live
kube-system Namespace UID before mutation; Read/List do not need this grant.
Administrator: add a ClusterRole rule apiGroups:[""], resources:["namespaces"],
resourceNames:["kube-system"], verbs:["get"], with a ClusterRoleBinding to the
EXACT authenticated identity. A namespace RoleBinding cannot grant this read.
This Helm prerequisite also affects legacy namespace-only credentials. Grant
chart resource and Helm release Secret permissions separately and narrowly.
Direct Kubernetes: generate a stable random UUIDv4 once, e.g. `uuidgen -r`,
lowercase it and persist it for this cluster. Replace the example UUID below.
Audience MUST be urn:formae:kubernetes:<canonical-lowercase-RFC4122-UUID>.
Broker allowlist: that exact audience or reserved "urn:formae:kubernetes:*".
This selector is not a general glob. Configure kube-apiserver, for example:
  --oidc-issuer-url=https://oidc.cloud.formae.ai
  --oidc-client-id=urn:formae:kubernetes:39c24d1d-3815-4817-9242-4032be46601b
  --oidc-username-claim=sub --oidc-username-prefix=formae:
  --oidc-signing-algs=RS256
Use --oidc-ca-file only if the issuer requires a custom trusted CA. The API
server must reach issuer discovery/JWKS HTTPS; the agent reaches cluster HTTPS.
Configure a narrow Role/RoleBinding for workloads and separate namespace grant
as administrator. Binding subject: kind: User, apiGroup: rbac.authorization.k8s.io,
name: formae:EXACT_PERSISTED_SUBJECT (replace the suffix with onboarding's exact
subject). Never grant a guessed subject or system:masters. The optional Helm
ClusterRoleBinding uses that same formae:EXACT_PERSISTED_SUBJECT user.
A correctly signed other subject can authenticate but fail RBAC (403); bad
audience/signature/expiry fail authentication. Verify actual identities and
grants before onboarding production. Key rotation needs reachable JWKS and
acceptance of a new signing key; this is not an immediate old-key revocation promise.

Evaluate locally with `pkl project resolve .` then `pkl eval main.pkl`. Replace all fictitious metadata before applying.
