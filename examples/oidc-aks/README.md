# Installation OIDC: AKS

The complete prerequisites are also embedded in `main.pkl`, because MCP returns Pkl files only.

Installation OIDC prerequisites (these comments are part of the MCP example).
This unreleased example uses the actual checkout schema via PklProject:
  ["k8s"] = import("../../schema/pkl/PklProject")
In your own PklProject, AFTER a compatible release is published, replace that
whole entry with ["k8s"] { uri = "package://hub.platform.engineering/plugins/k8s/schema/pkl/k8s/k8s@RELEASE_VERSION" }
Replace RELEASE_VERSION with the published compatible plugin version; the
current checkout's manifest version is NOT a claim that OIDC is released.
Also add ["formae"] { uri = "package://hub.platform.engineering/plugins/pkl/schema/pkl/formae/formae@0.89.0" }.
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
AKS (public Azure only): create an Entra federated identity credential with
issuer https://oidc.cloud.formae.ai, exact persisted subject, and audience
api://AzureADTokenExchange. Broker allowlist: "api://AzureADTokenExchange".
AzureOidcCredentials uses public tenant ID and application/client ID. The
cluster access grant must name the SERVICE PRINCIPAL OBJECT ID, not client ID
or tenant ID. An administrator resolves it, then grants appropriate Azure RBAC
for Kubernetes or Kubernetes RBAC for the cluster's configured authorizer.
Runtime scope is 6dae42f8-4368-4678-94ff-3960e28e3630/.default (AKS audience),
not ARM/Graph. Explicit OIDC requires endpoint AND certificateAuthority and
never calls list-admin/user-credentials or ambient metadata lookup. If the
Azure resource schema has no CA output, have an administrator supply the public
endpoint and base64 PEM CA from trusted cluster metadata; do not copy auth data.
Include namespace and workload grants. Verify object-ID identity, denial and
refresh on real AKS before release; live acceptance remains a release gate.

Evaluate locally with `pkl project resolve .` then `pkl eval main.pkl`. Replace all fictitious metadata before applying.
