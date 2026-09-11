# Connect matrix

Test scaffolding for one question: **can formae connect to a Kubernetes
cluster it did not create in the same forma?**

That is the hosted-formae case. In formae cloud a cluster is either already
there when the customer arrives, or formae discovers it, and the Kubernetes
target has to be authored afterwards, in its own file, its own stack, its own
apply. The shipped `examples/clusters/*.pkl` modules never test this: they
emit the cluster and its target together, so every reference resolves inside
one forma.

Each cloud gets two files, applied separately:

| File | What it does | Stack |
|------|--------------|-------|
| `<cloud>-cluster.pkl` | Cluster-side resources only. **No Kubernetes target.** | `connect-matrix-<cloud>-cluster` |
| `<cloud>-connect.pkl` | Kubernetes target + one probe namespace. **Names the cluster; nothing else.** | `connect-matrix-<cloud>-connect` |

The cluster files reuse the existing `@clusters/<cloud>.pkl` modules function
by function, minus the Kubernetes parts, so there is no second copy of the
VPC/IAM/networking wiring to keep in sync.

## The connect files name the cluster

They carry no endpoint and no CA bundle:

```pkl
auth = new k8s.EKSAuth {
  clusterName = properties.clusterName.value
  region = properties.region.value
}
```

The plugin reads the endpoint and CA off the cloud. That is what lets one
file serve all three scenarios below, and it is why none of them needs a
cross-stack reference: a cluster name and a region are literals a human
already has, from the console or from `formae resources`.

`kubernetesVersion` is optional the same way: these files set it because
they import a pinned `@k8s/v1.34/` tree and an extract should render back
into the same one. Omit it and the plugin asks the cluster. Either way the
field gate follows the cluster, never the declaration.

Stating `endpoint` and `certificateAuthority` explicitly still works and
always wins — that is the path for an air-gapped cluster, a custom endpoint,
or anything formae does not model. `examples/clusters/*.pkl` take that path,
because they create the cluster in the same forma and can reference it
directly.

## Running it

### Scenario A: formae creates the cluster

```sh
# 1. stand up the cluster (10-15 min on EKS/GKE, ~8 on AKS)
formae apply --mode reconcile --yes examples/connect-matrix/aws-cluster.pkl

# 2. connect to it from a separate stack
formae apply --mode reconcile --yes examples/connect-matrix/aws-connect.pkl \
  --prop cluster-name=eks-connect-matrix --prop region=eu-central-1

# success == the namespace exists
kubectl get ns formae-connect-probe

# 3. tear down, connect stack first
formae destroy --on-dependents=cascade --yes examples/connect-matrix/aws-connect.pkl
formae destroy --on-dependents=cascade --yes examples/connect-matrix/aws-cluster.pkl
```

### Scenario B: the cluster already exists

Identical, minus step 1. Point the props at the cluster:

```sh
formae apply --mode reconcile --yes examples/connect-matrix/gcp-connect.pkl \
  --prop project=<project> --prop location=<zone> --prop cluster-name=<name>

formae apply --mode reconcile --yes examples/connect-matrix/azure-connect.pkl \
  --prop resource-group=<rg> --prop cluster-name=<name>
```

### Scenario C: discover, adopt, then connect

The hosted case in full. The cluster predates formae, discovery finds it, it
is brought under management, and only then does a target get authored.

```sh
formae discover --target <cloud-target>
formae resources --query 'type:AWS::EKS::Cluster'
formae apply --mode reconcile --yes examples/connect-matrix/aws-connect.pkl \
  --prop cluster-name=<name from the listing> --prop region=<region>
```

Discovery does not merely record identity: it runs a sync changeset through
the changeset executor, which reads each resource through its plugin and
persists full properties. That is the same state `formae extract` reads.
Since the connect files need only a name, though, none of that has to be
complete for the connect to work — which is the point.

For reference, what each cloud's discovery *does* persist for a cluster:

| Cloud | Endpoint | CA bundle | Where the CA comes from |
|-------|----------|-----------|-------------------------|
| **EKS** | `Endpoint` | `CertificateAuthorityData` | Both read-only properties on the CloudControl resource model. `AWS::EKS::Cluster` is not discovery-filtered (the filter in `aws.go` targets Automode child resources). |
| **GKE** | `endpoint` | `masterAuth.clusterCaCertificate` | `clusterResponseTransformer` copies every field of the API response verbatim. |
| **AKS** | `fqdn` | `certificateAuthority` | **Not on the ARM resource at all.** `Read` synthesizes it from `ListClusterAdminCredentials` and swallows every error, so a hardened cluster can land in state with an empty CA. |

That last row is why the connect files no longer read the CA out of stored
state. The plugin fetches it at connect time, preferring the lower-privilege
`ListClusterUserCredentials`, and reports a real error when it cannot.

### Prerequisites

Same as `examples/clusters/`: `GCP_PROJECT` + `GCP_APPLY_AS` for GCP,
`AZURE_SUBSCRIPTION_ID` + `AZURE_PRINCIPAL_ID` for Azure, an AWS profile the
agent can assume for AWS. On top of the cluster RBAC each cloud already
needed, the lookup wants read access to the cluster record:

| Cloud | Permission |
|-------|------------|
| EKS | `eks:DescribeCluster` |
| GKE | `container.clusters.get` |
| AKS | `Microsoft.ContainerService/managedClusters/listClusterUserCredential/action` |
| OVH | none, the plugin already fetches a kubeconfig for the token |

`./scripts/eval-examples.sh` evaluates all six files with dummy values and no
cloud access.

## What we know today

Verified by `pkl eval` on these files plus unit tests driving `pkg/config`'s
own parser and the EKS token path end to end with static dummy credentials.
**Not** verified end to end: no agent was running, so no cluster was built
and no apply was attempted.

Before this scaffolding existed, a target authored in a separate forma was
rejected on two of three clouds, because the endpoint, CA and cluster name
all had to be referenced out of the cluster's stack and only two of those
fields could survive the trip:

```
failed to parse EKS auth config: json: cannot unmarshal object into Go struct field EKSAuthConfig.ClusterName of type string
failed to parse AKS auth config: json: cannot unmarshal object into Go struct field AKSAuthConfig.ResourceGroup of type string
```

Two changes removed that class of problem rather than widening it. The
plugin no longer unwraps reference envelopes at all: formae resolves a
reference to its scalar before a plugin sees it, so an envelope arriving here
means resolution failed and is now reported by name. And the connection
details are derived instead of referenced, so the common case has no
references left to fail.

## Open questions

1. **Does a reference resolve across stacks inside a target config?** No
   longer on the critical path, since these files reference nothing, but it
   still decides whether `examples/clusters/*.pkl` can be split the same way.
2. **Which principal ends up cluster-admin?** These files create the cluster,
   so the agent's identity is the creator and gets admin for free. A cluster
   that already existed needs an access entry, IAM binding or role assignment
   granted by someone who is already admin. Not covered here, and not
   something a hosted flow can bootstrap on its own.
3. **What does discovery label a pre-existing cluster?** Only matters now for
   reading the name out of `formae resources`.

## Notes

- Resource labels inside `@clusters/*.pkl` are not slug-scoped
  (`eks-cluster`, `gke-cluster`, `aks-cluster`).
- A cross-stack `Resolvable` **must** set `stack`. `forma.pkl`'s renderer
  fills a null `stack` from a resource in the same forma and throws when
  there is none.
- `formae.Prop` has no `description` field: `flag`, `default`, `type`,
  `value` only. Prop documentation goes in a comment.
- Prior art for the split lives in `customers/dqc/pe-formae`
  (`vars.pkl:44-74`): a cross-stack, cross-plugin `ManagedClusterResolvable`
  into the AKS stack, referencing only `fqdn` and `certificateAuthority` and
  passing `resourceGroup`/`clusterName` as literals. Its
  `docs/E2E-REPORT.md` records the in-cluster half as plan-clean but never
  applied.
