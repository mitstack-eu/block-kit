# Observing resources with the observe package

This page is for a block author choosing how each of a block's resources is
observed. An observer is the code that asks the provider whether one resource
stands, and the handler calls one for each declared resource on every
lifecycle call. The kit's `observe` package ships observers for common
resource types and the listers that find everything a subscription holds at a
provider, and this page covers both.

## What an observer answers

An observer is a `handler.Observer`, with one method,
`ObserveResource(ctx, ref)`. It asks the provider what stands right now and
never answers from what an ensure remembered, because a resource can vanish
behind the block's back. It returns a `contract.Observation`:

| Field | What it holds |
|---|---|
| `State` | `contract.Present`, `contract.Absent`, or `contract.Unknown` when this observation cannot tell, such as while a server is still booting |
| `Outputs` | The values the resource's type declares, such as a host's `publicIp`, which other blocks of the app may read |
| `SecretOutputs` | The outputs the type marks secret, which the handler keeps out of the engine's records |
| `StatusDetail` | A short reason, shown to people, for a state other than present |
| `Identity` | The values the observer read the resource by. Nil keeps the identity it was handed, and one holding a request secret is refused |
| `Redrive` | Read only beside `Unknown`, as [Serving lifecycle calls](handler.md#work-that-outlasts-one-call) explains |

The `contract.ResourceRef` an observer receives carries the resource's name,
its type, its identity and the call's `contract.Desired`. Its `Unsubscribing`
field is true only when the handler observes the resource to prove it absent
after an unsubscribe's delete. A resource's identity
is the set of values that address it at the provider, such as
`{"instanceId": "i-0abc"}`, which `EnsureResources` reported when it created
the resource. Every shared observer answers `Unknown` without calling the
provider when the identity lacks the key it needs, and reports that identity
on a present answer.

## The observers the kit ships

Each shared observer is a struct whose `API` field holds a small interface you
implement over your own provider client. `observe.HetznerFirewall`, for
example, needs only `FirewallExists(ctx, d, firewallID)`, which the example
block's Hetzner client implements in a few lines.

| Observer | Type it observes | Identity keys | What it reports |
|---|---|---|---|
| `EC2Instance` | `aws.ec2-instance` | `instanceId` | Present when running, absent when terminated or missing, with `hostId`, `execKind`, `execTarget`, `publicIp` and `cloud.region` |
| `EgressProxy` | `aws.egress-proxy` | `instanceId` | As `EC2Instance`, with no outputs |
| `S3Bucket`, `VPC`, `SecurityGroup`, `SSMAssociation` | `aws.s3-bucket`, `aws.vpc`, `aws.security-group`, `aws.ssm-association` | `bucket`, `vpcId`, `groupId`, `associationId` | Whether it exists |
| `EBSVolume` | `aws.ebs-volume` | `volumeId` | Present when available or in use, absent when deleted or missing |
| `ECRRepository` | `aws.ecr-repository` | `repositoryName` | `registryHost`, the host of the repository's URI |
| `IAMRole` | `aws.iam-role` | `roleName` | `roleArn`, as a secret output, because the type marks it secret |
| `Route53Record` | `aws.route53-record` | `hostedZoneId`, `recordName` | `appHostname` and its `https` `appUrl` |
| `EC2Container` | `aws.ec2-container` | `instanceId`, `container` | The container's state, read from the instance's Docker |
| `HetznerServer` | `hetzner.server` | `serverId` | Present when running, with `hostId`, `execKind`, `execTarget` and `cloud.region` |
| `HetznerPrimaryIP` | `hetzner.primary-ip` | `primaryIpId` | `publicIp` |
| `HetznerFirewall`, `HetznerNetwork` | `hetzner.firewall`, `hetzner.network` | `firewallId`, `networkId` | Whether it exists |
| `GitHubRepository` | `github.repository` | `repo` | `repo`, the repository's full name |
| `GitHubCommit` | `github.commit` | `repo`, `sha` | `sourceSha`, and `sourceBranch` when the identity holds a `branch` |
| `NodeReported` | A resource the platform cannot reach, such as `kubernetes.namespace` | `kind`, `name`, and `argocdApplication` for an object Argo CD applied | Presence from the cluster's node reports, as below |

`EC2Container` runs a short probe script on the instance through a
`HostCommander`, the interface you implement to run a script on an EC2
instance and return its output, such as one built on `helpers/hostexec`. It
reports a container absent only when Docker answers that no such container
exists, and empty output is unknown. A script that exits nonzero is an error.

## Observing a type that carries an observe spec

The type snapshot is the list of resource types the platform serves, which a
block commits as `type-snapshot.json` and hands the handler as `Config.Types`.
Some types in it carry an `observeSpec`, the catalogue's machine-readable
account of how to find and read a resource of the type. For
such a type you write no observer. `observe.KitObserver` runs the spec, and the
handler hands it every resource of the type. It reads three things only: the
type, the identity the block recorded and the subscription's id. A bucket made
by Go code, by an OpenTofu module or by hand gets the same answer.

`aws.s3-bucket` carries this spec, which the kit observer runs:

```json
{"find": {"tag": "mitstack:subscription", "identity": "bucket"},
 "probe": {"reads": ["tags", "existence"], "maxReads": 4},
 "fullRead": {"cloudControl": "AWS::S3::Bucket"}}
```

You set `Config.KitObserver` to an `observe.KitObserver` holding the provider
interface the spec needs, which `handler.New` refuses to go without:

| Field | Serves a spec whose `fullRead` names | Methods you implement, one provider request each |
|---|---|---|
| `AWS` | `cloudControl`, an AWS Cloud Control type | `Exists` and `Tags` for the probe, `GetResource` for the full read, and `ResourceARNs` to list by tag |
| `Hetzner` | `collection`, a Hetzner API path such as `/servers/{id}` | `Get` for the path with the identity in place of `{id}`, and `ListByLabel` to list by label |

A health check is a probe, which reads the resource the recorded identity names
and the tag on it, within the type's `maxReads`. When that resource's last full
read is six hours old, or the handler holds no time for it, the check is a full
read instead. The handler keeps those times in memory, so a restarted operator
reads each resource fully at its next check.

To check attributes the probe cannot see, such as a bucket's versioning, give
the `Resource` you pass as `Config.Resource` a method
`CheckFullRead(ctx, ref, read)`, which makes it a `handler.FullReadChecker`.
The handler calls it with each full read that found the resource, holding the
provider's whole document in `read.Properties`. A non-empty problem it returns
answers the resource unhealthy.

The answer follows from whether the tag and the recorded identity agree:

| What the observer finds | Answer |
|---|---|
| The recorded identity exists and carries the subscription's tag | Present |
| It exists but lacks the tag, or carries another subscription's | Unknown, with a status detail that names the tag and says the resource is unhealthy |
| It does not exist, and nothing else carries the tag | Absent |
| It does not exist, but an object no recorded identity names carries the tag | Unknown, naming that object as unrecorded. An unsubscribe then cannot finish, and the platform keeps asking until a person removes the object |
| The block recorded no identity | Unknown, with no provider call |

To find unrecorded objects, the observer lists what carries the tag, which is
one more provider call. It lists on every full read, and on any check that
finds the recorded identity gone, a probe included, because only the listing
can keep an absent answer from ending the subscription. A check finds a
resource gone mostly during an unsubscribe. ADR 0108 leaves this listing out of
a probe's reads, so it does not count against `maxReads`. A full read names
each unrecorded object in the status detail beside a present answer. The kit
observer only reads, so it never deletes an object it found by tag.

## Observing a resource the platform cannot reach

`NodeReported` serves a resource that lives inside a cluster the platform has
no route to, such as a `kubernetes.namespace`. A node report is the report a
node of the cluster sends the platform, which the platform serves at
`GET /v1/subscriptions/{id}/node-reports`. The resource's identity names the
object's `kind`, `name` and optional `namespace`, and `clusterSubscription`,
the subscription of the cluster whose reports to read. Without it,
`NodeReported` reads the observing subscription's own reports.

An object matches only when it also carries the observing subscription's id,
since the subscription that creates an object labels it. A report's body lists
those objects under `subscriptionResources`. It may also carry `madeAt`, when
the node began reading, and one of two ways to say what it read in full:
`subscriptionResourcesComplete: true` for the whole list, or
`subscriptionResourceKinds` for the kinds whose read finished. A kind is the
object's type, written as the report lists it, such as `Namespace`, and the
identity's `kind` must match it exactly. A node that may not read every kind,
such as one with no right to read a Secret, names the kinds it did read:

```json
{
  "madeAt": "2026-10-23T09:00:00Z",
  "subscriptionResources": [
    {"kind": "Namespace", "name": "app-alpha", "namespace": null, "subscription": "sub-1"}
  ],
  "subscriptionResourceKinds": ["Namespace", "ServiceAccount"]
}
```

A report counts when its `madeAt`, or its receipt time when it has none, is
after `Desired.VerbAt`, and the newest counting report decides:

- An object the report lists is present.
- An object it does not list is absent only when the report carries a `madeAt`
  after `VerbAt` and either marks its whole list complete or names the
  object's kind in `subscriptionResourceKinds`.
- Anything else is unknown, so an object of a kind the node never read stays
  unknown rather than reading absent.

### An object Argo CD applied

A node may have no right to read an object, such as a Secret, that Argo CD
applied from Git. Argo CD is the controller that applies a cluster's Git
repositories, and each of its Applications lists the objects it applied, with
no contents. When the resource's identity also holds `argocdApplication`, the
name of the Application that applies the object, `NodeReported` reads that
Application's list instead of `subscriptionResources`. The report carries it
under `argocd.applications`, each entry with its `sync` status and
`resources`:

```json
{
  "madeAt": "2026-10-23T09:00:00Z",
  "argocd": {"applications": [
    {"name": "repository-credentials", "health": "Healthy", "sync": "Synced", "resources": [
      {"group": "", "kind": "Secret", "namespace": "argocd", "name": "app-repository-alpha", "sync": "Synced"}
    ]}
  ]}
}
```

The identity's `kind` names the object as `kubectl` does, its kind in lower
case followed by a dot and its API group when it has one, such as `secret` or
`role.rbac.authorization.k8s.io`. The newest counting report decides, as
above:

- An object the Application's `resources` lists, by kind, API group,
  namespace and name, is present.
- An object it does not list is absent only when the report carries a
  `madeAt` after `VerbAt` and shows the Application `Synced`, since a synced
  Application that prunes holds nothing its Git no longer names.
- Anything else is unknown, including a report that names no such Application
  or carries no `resources` for it.

`observe.NodeReportsClient` reads the reports from the platform the request's
events callback names, with the callback's token, so a block writes no client
of its own:

```go
observe.NodeReported{API: observe.NodeReportsClient{}}
```

`NodeReported` reports presence and identity only, so a block that provides
such a resource reports the type's other required outputs from an observer of
its own.

### A cluster that has sent no report

A node spends its cluster's single-use enrolment token before it can send any
report, so the enrolment reaches the platform first. The platform serves when
that happened as `enrolledAt` beside the reports, or `null` while no node has
enrolled. When no report counts, `NodeReported` answers unknown and adds what
the enrolment shows, so the subscription's events say whether to look at the
node's boot and enrolment or at its reporting:

| `enrolledAt` in the platform's answer | The unknown answer reads |
|---|---|
| A time | `resource "kubernetes.namespace": no node report made since the verb has arrived, though cluster subscription "sub-1" enrolled a node at 2026-10-07T11:00:00Z` |
| `null` | `resource "kubernetes.namespace": no node report made since the verb has arrived, and cluster subscription "sub-1" has enrolled no node with the platform` |
| Missing, as from a platform that does not serve it | `resource "kubernetes.namespace": no node report made since the verb has arrived` |

`observe.NodeReportsClient` reads the enrolment in the same request as the
reports. A block that passes an API of its own reads it only when that API
also implements `observe.ClusterReportsAPI`, and otherwise keeps the last
row's answer.

## Listing what a subscription holds

Every object a block creates carries the subscription's id as a tag, or the
provider's equivalent, so that the platform can find and tear down whatever a
subscription left. A lister finds every object carrying one subscription's
id, and `kittest.AssertBlock` uses one to prove each resource is tagged and
nothing else is, as [Testing a block](testing.md) describes. Each lister is an
`observe.TaggedLister` and returns each object as an `observe.Tagged`, its
type and the id that names it.

| Lister | What it lists |
|---|---|
| `observe.AWSTagged` | Every AWS resource carrying the tag `mitstack:subscription` (`observe.AWSSubscriptionTag`), typed from its ARN, such as `aws.iam-instance-profile`, `aws.elastic-ip`, `aws.security-group-rule`, `aws.dlm-lifecycle-policy` or `aws.ssm-document` |
| `observe.HetznerLabelled` | Every Hetzner Cloud object carrying the label `mitstack.dev/subscription` (`observe.HetznerSubscriptionLabel`) |
| `observe.GitHubTagged` | What a subscription holds on GitHub, as the next paragraphs describe |

`observe.GitHubTagged` works over `observe.GitHubTagAPI`, which your GitHub
client or your fake GitHub implements. A repository belongs to the
subscription when it carries the topic `observe.GitHubSubscriptionTopic`
gives, which for `sub_3e304e324312` is `mitstack-sub-3e304e324312`. On such a
repository the lister also reports:

- Every webhook.
- Each Actions secret and variable whose name the block lists in the lister's
  `Secrets` or `Variables` fields, so another block's secret on the same
  repository is not counted.
- Each path written by a commit whose message closes with the trailer
  `observe.GitHubSubscriptionTrailer` gives, as a `github.workflow-file`.

A block may also write to a repository it did not create, the one its
`github.repository` or `github.gitops-repository` requirement binds. On that
repository the lister reports the same objects, except that a webhook counts
only when it points at the lister's `ProcessorURL`, and the repository itself
counts only when it carries the topic. When the lister's `CommitType` is set,
a repository's trailer commits count once, as that type, while the branch
still holds a path they wrote.

A resource whose type carries no tag of its own still needs a listed object,
so its lister finds it by whatever stands in for the tag.
`mitstack.app-hostname` is one such type: a Route 53 record carries no tag, so
the lister finds the record by its name, which no other app holds.
