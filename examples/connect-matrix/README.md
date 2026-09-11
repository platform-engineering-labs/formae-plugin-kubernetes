# Connect matrix

Test scaffolding for one question: **can formae connect to a Kubernetes
cluster it did not create in the same forma?**

That is the hosted-formae case. In formae cloud a cluster is either already
there when the customer arrives, or formae discovers it, and the Kubernetes
target has to be authored afterwards — in its own file, its own stack, its
own apply. The shipped `examples/clusters/*.pkl` modules never test this:
they emit the cluster and its target together, so every reference resolves
inside one forma.

Three scenarios, one pair of files per cloud. Scenario A is what the shipped
examples already cover; B and C are the hosted-formae shapes nothing covered
before.

Each cloud gets two files, and they are meant to be applied separately:

| File | What it does | Stack |
|------|--------------|-------|
| `<cloud>-cluster.pkl` | Cluster-side resources only. **No Kubernetes target.** | `connect-matrix-<cloud>-cluster` |
| `<cloud>-connect.pkl` | Kubernetes target + one probe namespace. Names the cluster by `(stack, label)`. | `connect-matrix-<cloud>-connect` |

The cluster files reuse the existing `@clusters/<cloud>.pkl` modules function
by function, minus the Kubernetes parts, so there is no second copy of the
VPC/IAM/networking wiring to keep in sync.

## Running it

### Scenario A: formae creates the cluster

```sh
# 1. stand up the cluster (10–15 min on EKS/GKE, ~8 on AKS)
formae apply --mode reconcile --yes examples/connect-matrix/aws-cluster.pkl

# 2. connect to it from a separate stack
formae apply --mode reconcile --yes examples/connect-matrix/aws-connect.pkl

# success == the namespace exists
kubectl get ns formae-connect-probe

# 3. tear down, connect stack first
formae destroy --on-dependents=cascade --yes examples/connect-matrix/aws-connect.pkl
formae destroy --on-dependents=cascade --yes examples/connect-matrix/aws-cluster.pkl
```

### Scenario B: the cluster already exists

Same connect file, two props. Point it at whatever stack and label discovery
produced:

```sh
formae apply --mode reconcile --yes examples/connect-matrix/aws-connect.pkl \
  --prop cluster-stack=unmanaged \
  --prop cluster-label=<label discovery assigned>
```

Azure additionally takes `--prop resource-group-label=<label>`, because
`AKSAuth` needs the resource group as well as the cluster.

### Scenario C: discover, adopt, then connect

The hosted case in full. The cluster predates formae, discovery finds it,
it is brought under management, and only then does a Kubernetes target get
authored against it.

```sh
# 1. discovery enumerates the cloud account
formae discover --target <cloud-target>

# 2. find what discovery called the cluster
formae resources --query 'type:AWS::EKS::Cluster'

# 3. connect against the discovered resource, wherever it landed
formae apply --mode reconcile --yes examples/connect-matrix/aws-connect.pkl \
  --prop cluster-stack=unmanaged \
  --prop cluster-label=<label from step 2>
```

Discovery does not merely record identity: it runs a sync changeset through
the changeset executor, which reads each resource through its plugin and
persists full properties. That is the same state `formae extract` reads to
emit forma, and it is what a `$ref` into a discovered cluster resolves
against.

So the question is whether the properties a Kubernetes target needs survive
the trip. They are not the same properties on every cloud:

| Cloud | Endpoint | CA bundle | Where the CA comes from |
|-------|----------|-----------|-------------------------|
| **EKS** | `Endpoint` | `CertificateAuthorityData` | Both are read-only properties on the CloudControl resource model, returned by GetResource. `AWS::EKS::Cluster` is not discovery-filtered (the filter in `aws.go:66-81` targets Automode child resources). |
| **GKE** | `endpoint` | `masterAuth.clusterCaCertificate` | `clusterResponseTransformer` copies every field of the API response verbatim (`pkg/resources/container/transformers.go:11-14`). |
| **AKS** | `fqdn` | `certificateAuthority` | **Not on the ARM resource at all.** `Read` synthesizes it by calling `ListClusterAdminCredentials` and parsing the admin kubeconfig (`pkg/resources/managedcluster.go:64-89`). |

AKS is the one that can quietly come up short. That call needs
`Microsoft.ContainerService/managedClusters/listClusterAdminCredential/action`,
it fails outright on a cluster with `disableLocalAccounts: true`, and the
plugin swallows every error by design:

```go
resp, err := mc.api.ListClusterAdminCredentials(ctx, rgName, clusterName, nil)
if err != nil {
    return ""
}
```

A discovered AKS cluster can therefore land in state with
`certificateAuthority: ""`. The target's reference then resolves — to an
empty string. Not an unresolved envelope, a resolved empty one, which no
reference guard can catch. `requireAuthFields` is what stops it now:

```
AKS auth config missing required field(s): CertificateAuthority
```

Before that check the empty CA decoded to empty `CAData` and the client fell
back to system roots, surfacing as an x509 error naming nothing.

**Mitigation worth knowing:** once a cluster is adopted into a managed stack,
the Kubernetes target can be authored in the same forma as the adopted
cluster, which sidesteps cross-stack resolution entirely. If Scenario B turns
out not to resolve, this is the fallback that still gives the hosted flow a
working path.

### Prerequisites

Same as `examples/clusters/`: `GCP_PROJECT` + `GCP_APPLY_AS` for GCP,
`AZURE_SUBSCRIPTION_ID` + `AZURE_PRINCIPAL_ID` for Azure, an AWS profile the
agent can assume for AWS. `./scripts/eval-examples.sh` evaluates all six
files with dummy values and no cloud access.

## What we know today

Verified in-session by `pkl eval` on these files plus unit tests driving
`pkg/config`'s own parser with the exact JSON these files render. **Not**
verified end-to-end: no agent was running, so no cluster was built and no
apply was attempted.

Before this session, EKS and AKS could not parse a target config whose
cluster identifiers were cross-stack references:

```
failed to parse EKS auth config: json: cannot unmarshal object into Go struct field EKSAuthConfig.ClusterName of type string
failed to parse AKS auth config: json: cannot unmarshal object into Go struct field AKSAuthConfig.ResourceGroup of type string
```

GKE parsed, because it referenced only `Endpoint` and `CertificateAuthority`
— the two fields typed `ResolvedString`, a plugin-local type that unwrapped
the envelope.

### The envelope was never the plugin's to unwrap

formae resolves a reference and replaces it with its scalar value before the
config reaches a plugin (`internal/metastructure/resolver/resolver.go`,
`toPluginFormat`). A reference it could **not** resolve is skipped and passed
through structurally intact, with no `$value`:

```go
if ref.ResolvedValue.Value == nil {
    continue
}
```

So an envelope arriving at the plugin is not data — it is a resolution that
failed upstream. `ResolvedString` read `$value` out of it, found nothing, and
produced `""`. An EKS token minted with an empty cluster name, an AKS token
with no resource group, and a 401 from the API server naming none of it.

`ResolvedString` was introduced on 2026-06-03 in `6eb31718`
("feat: lgtm-observability example + service LB resolvables"), scoped to two
fields from the start.

### What changed in this repo

1. `pkg/config/config.go` — `ResolvedString` deleted. Every auth field is a
   plain `string` again. `FromTargetConfig` now calls
   `guardNoUnflattenedReferences`, which rejects an Auth block still carrying
   a `$res`/`$ref` object and names every offending field:

   ```
   target config Auth carries unresolved formae reference(s) in ClusterName:
   formae flattens a resolved reference to its value before the plugin sees
   it, so this reference did not resolve — check that the referenced resource
   exists in the stack the reference names
   ```

2. `schema/pkl-main/target.pkl` — `GKEAuth` gained optional `projectId`,
   `location` and `clusterName` (regenerated into `schema/pkl/target.pkl`).
   The Go struct read all three and keyed its token cache on them; Pkl had no
   way to set any, so the fields were dead and the cache key was
   `GKE|<endpoint>|||`. `examples/clusters/gcp.pkl` and `gcp-connect.pkl` now
   set them.

3. `pkg/config/config_test.go` — `TestFromTargetConfig_RejectsUnflattenedReference`
   (one subtest per auth type), `..._NamesEveryUnflattenedReference` so one
   apply does not need six round trips to find six broken references, and
   `..._AllowsPlainScalars` to guard the guard.

Green: `go test -tags unit ./...` 320 passed, `make lint` 0 issues,
`make verify-schema` passed, `./scripts/eval-examples.sh` 41 forma checked /
0 failed.

## Open questions the run answers

1. **Does formae resolve a cross-stack `$ref` inside a target config at all?**
   This is now the whole question. The plugin no longer papers over a failure
   to resolve; if the reference does not resolve, the apply stops with the
   message above naming the field. If it does resolve, the plugin sees plain
   strings and every cloud works. There is no third outcome, and the run
   tells them apart immediately.
2. **What does discovery label a pre-existing cluster?** The connect files
   take the label as a prop because nobody here knows yet.
3. **Does a reference into the `unmanaged` stack resolve like any other?**
   Discovery writes there, so the whole hosted flow depends on it.
4. **Which principal ends up cluster-admin?** These files create the cluster,
   so the agent's identity is the creator and gets admin for free. A cluster
   that already existed needs an access entry / IAM binding / role assignment
   granted by someone who is already admin — not covered here, and not
   something the hosted flow can bootstrap on its own.

## Notes

- Resource labels inside `@clusters/*.pkl` are not slug-scoped
  (`eks-cluster`, `gke-cluster`, `aks-cluster`), which is why the connect
  files can default to them.
- A cross-stack `Resolvable` **must** set `stack`. `forma.pkl`'s renderer
  fills a null `stack` from a resource in the same forma and throws when
  there is none.
- `formae.Prop` has no `description` field — `flag`, `default`, `type`,
  `value` only. Prop documentation goes in a comment.
- `ResolvedString` is gone. If a future config needs to carry an unresolved
  reference into a plugin on purpose, that is a formae-core contract change,
  not a per-plugin string type — every plugin would otherwise reinvent it.
- Prior art for this split lives in `customers/dqc/pe-formae`
  (`vars.pkl:44-74`): a cross-stack, cross-plugin `ManagedClusterResolvable`
  into the AKS stack. It references only `fqdn` and `certificateAuthority`
  and passes `resourceGroup`/`clusterName` as literals — routing around the
  defect above. Its `docs/E2E-REPORT.md` records the in-cluster half as
  plan-clean but never applied, so nobody has run cloud auth against a live
  cluster from a separate stack yet.
