# Writing a block's manifest

This page is for a block author writing `manifest.yaml`, the file that tells
mitstack what a block is and what it creates. It shows a complete manifest,
says what each key means, lists the keys the parser refuses, and explains
block ids, resource types and the type snapshot. The kit's `contract` package
reads every manifest, through `contract.ParseManifest`, and blockverify, the
handler and the platform all judge a manifest by that one parser.

## A complete manifest

The example block's manifest in
[`examples/hetzner-firewall`](../examples/hetzner-firewall/manifest.yaml) is
the smallest a block needs. The manifest below uses every key a block is
likely to need. It runs an API container on a host the app already has, and
it writes a deploy key into the app's GitHub repository:

```yaml
apiVersion: mitstack/v1beta
type: block
metadata:
  id: "@acme/shop-api"
  name: Shop API
  owner: acme
spec:
  version: 1.2.0
  releaseClass: feature
  releaseNote: 1.2.0 serves the API over HTTP/2.
  description: Runs the shop's API container on the host the app provides.
  requires:
    - github.repository
    - { name: host, type: [aws.ec2-instance, hetzner.server] }
  resources:
    - { name: api, type: [aws.ec2-container, hetzner.server-container] }
    - { name: deploy-key, type: github.actions-secret }
  provides: [api]
  receivesAppSecrets: true
  lifecycle:
    endpoint: https://operators.acme.example/shop-api/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
    needsCredentialRefresh: true
    settleBudget: 20m
    verbConnections:
      validate: [github]
      unsubscribe: [github, hetzner]
```

## What each key means

A key marked required must be present, and the parser refuses the manifest
without it, naming the key's path.

| Key | Required | What it says |
|---|---|---|
| `apiVersion` | Yes | Always `mitstack/v1beta`, the one manifest version the kit reads |
| `type` | Yes | `block` for a service one app uses, or `capability` for one that serves several apps, such as a shared cluster |
| `metadata.id` | Yes | The block's id, which the signed token in every call for the block, the OBO token, names, as [Block ids](#block-ids) describes |
| `metadata.name` | Yes | The name people see |
| `metadata.owner` | No | The slug of the org that publishes the block, which the platform requires |
| `spec.version` | Yes | The block's version, such as `1.2.0` |
| `spec.releaseClass` | No | What a new version changes, which the platform reads as `correctness-fix`, `feature` or `breaking` |
| `spec.releaseNote` | No | One paragraph on what the version changes |
| `spec.description` | No | One sentence on what the block does |
| `spec.requires` | No | The resources the block needs from other blocks of the app, by exact type |
| `spec.resources` | Yes | The resources the block creates, by exact type |
| `spec.provides` | No | The names of the resources the block offers the rest of the app |
| `spec.receivesAppSecrets` | No | `true` sends the block every secret the app holds, under `Desired.AppSecrets`, for a block that hands them to the app's own runtime |
| `spec.observability` | No | Refused by the platform, which takes the metric series of each resource from its type |
| `spec.lifecycle.endpoint` | Yes | The HTTPS address of the operator's `/hook` route |
| `spec.lifecycle.operations` | Yes | The lifecycle calls the operator answers, usually all four, plus `logs` for a block that serves each subscription's own lines, as [Serving a subscription's logs](handler.md#serving-a-subscriptions-logs) explains |
| `spec.lifecycle.contract` | Yes | `kit/v1`, the lifecycle contract this kit serves and blockverify judges |
| `spec.lifecycle.needsCredentialRefresh` | No | `true` asks the platform for a periodic `validate`, as [The lifecycle keys](#the-lifecycle-keys) explains |
| `spec.lifecycle.settleBudget` | No | How long the platform waits for the block's resources to settle (reach the state a call asked for) |
| `spec.lifecycle.verbConnections` | No | The connections whose secrets a `validate` and an `unsubscribe` need |

The platform refuses more than the parser does when you publish a block, and
`contract.CheckManifest` holds those rules once, for the platform and for
blockverify's manifest suite alike. It refuses a `metadata.owner` that is not
an org slug, and a private id under another org's namespace. It refuses a
`metadata.name` outside 2 to 40 characters and a `spec.description` outside 10
to 240. It refuses a `spec.version` that is not strict semver, such as
`v1.2.0`, a `spec.releaseClass` outside the three above, and a
`correctness-fix` with no `spec.releaseNote`. It refuses an endpoint that is
not HTTPS, an operation outside `subscribe`, `unsubscribe`, `upgrade`,
`validate`, `rotate` and `logs`, an operation listed twice, `subscribe` without
`unsubscribe`, any `spec.observability`, and a type the platform does not
serve. blockverify judges the types against the block's `type-snapshot.json`.
The platform alone runs the checks that need its records, such as whether the
version is already published, and the dry-run apply reaches them.

blockverify compares `spec.version` with the base branch's manifest on a pull
request. A changed version needs both `spec.releaseClass` and
`spec.releaseNote`, as [Running the block checks](block-checks.md) describes.

## Requirements, resources and what a block provides

A block names what it needs and what it creates by exact resource type (a
type id from mitstack's type catalogue, such as `hetzner.firewall`). Each
entry under `requires` or `resources` is a type id on its own, as in
`github.repository`, or a mapping of `type` and `name`. Either mapping may
also set `many`. The `type` is one id or a list of ids, and an entry that
lists more than one type needs a `name`.

An entry without a name is named by its type id, and the block's code reads it
under that name. Two entries that would share a name are refused, and so is a
resource name holding a slash. A resource's name stays the same across
versions, because the platform records each resource under it.

A type id is a community type, such as `aws.s3-bucket`, or a private type that
an org keeps in the catalogue under its own name, such as
`@acme/aws.special-bucket`. The org part follows the rules for a private block
id, which [Block ids](#block-ids) gives. Write a private type id in quotes,
because YAML does not allow `@` at the start of an unquoted value. A private
type id holds a slash, so a resource of a private type needs its own `name`.

A requirement listing several types is met by whichever of them the app
offers. The request's `requirementTypes` says which type met each
requirement, so the block's code reads `requires.host` the same way whether
an `aws.ec2-instance` or a `hetzner.server` met it. A resource listing several
types is created as one of them, and the block reports which one when it
creates it.

With `many: true`, a requirement collects every resource of its types that the
app offers, as a list, and the block subscribes even when the list is empty.

With `optional: true`, a requirement takes the one resource of its types the
app offers, or none. It suits a block that serves apps with and without a
provider, such as a web app that reads a database only when the app has one:

```yaml
requires:
  - { name: database, type: hetzner.server-postgres-database, optional: true }
```

When the app offers none, the request binds nothing under the requirement's
name, so `Desired.Bindings` and `Desired.RequirementTypes` hold no entry for
it and the block's code runs without it. Two offers are refused, as for any
requirement. A requirement is never both `optional` and `many`, since
`many` already accepts an empty list. A resource entry takes no `optional`.
`kittest.CheckBlock` runs the block once more with each optional requirement
unbound, so a block that cannot run without its provider fails there.

A resource entry with `many: true` stands for zero or more members of its
type. It suits a resource the block creates a varying number of times, such as
the servers of a node pool. Here the block keeps one gateway and any number of
Cloud workers:

```yaml
resources:
  - { name: gateway, type: hetzner.server }
  - { name: cloud-worker, type: hetzner.server, many: true }
```

The block's `EnsureResources` reports each member under `<entry>/<key>`, such
as `cloud-worker/fsn1-1`, and reports none while the pool is empty. The key is
1 to 40 lowercase letters, digits and hyphens, and it names the member for its
whole life. The handler answers each member under its own name, observes it
through the entry's observer, here `Config.Observers["cloud-worker"]`, and
refuses a report under the entry's own name. A block marking an entry many
must implement `handler.ResourceReporter` or `handler.OwnSecretsReporter`,
because only a report names the members. `ManifestResource.IsMember` tells
whether a name is one of an entry's members, and `ManifestResource.Names` lists
the names an entry stands for among the names a request or a report holds, in
the order the handler observes them.

`provides` names resources from the block's own `resources`, and never an
entry marked many, because a binding names one resource. The outputs of a
provided resource, such as a host's address, reach the other blocks of the app
that require its type.

A binding is one value the engine hands the block from a resource. It is
written `requires.<reference>.outputs.<output>` for an output of the resource
that met a requirement, and `resources.<name>.outputs.<output>` for an output
of one of the block's own resources. Your code reads a requirement's bindings
in `Desired.Bindings`, keyed by the requirement's name, and those of a `many`
requirement in `Desired.BindingsAll`. `contract.CheckBinding` refuses a
binding that names a requirement or resource the manifest lacks, or an output
that a listed type does not declare.

## The lifecycle keys

`needsCredentialRefresh: true` asks the platform to send the block a
`validate` on a schedule while a subscription is active. An operator that
turns the request's short-lived cloud identity into AWS credentials can then
renew them before the identity's token expires. Inside the operator,
`helpers/credrefresh` or the `cloudid` package holds such a credential between
calls, as [The optional helpers](helpers.md) describes. A manifest without the
key reads as `false`.

To settle means the platform observes the state a call asked for, such as
every resource present after a subscribe. `settleBudget` says how long the
platform keeps waiting after a call, for a block whose install outlasts the
platform's default wait. It takes a positive Go duration such as `75m`, and
the parser refuses zero, a negative value or anything that is not a duration.

A connection is an account the org connected at a provider, such as its
Hetzner Cloud project or its GitHub organisation. A request carries each
connection's secrets in `Desired.Secrets`, under the connection's name, as in
`Desired.Secrets["hetzner"]["api_token"]`. `verbConnections` names, for
`validate` and `unsubscribe`, the connections that call needs. A probe and a
teardown often need less than an install, and the platform stores the
declaration so that it can send each call only what it needs. Until it does,
a `validate` and an `unsubscribe` carry the same secrets as a `subscribe`. The
parser refuses a
verb other than `validate` or `unsubscribe`, and a value that is not a list of
names.

## The sections of a capability manifest

A manifest whose `type` is `capability` and that connects an org to a vendor
holds four more sections under `spec`, which the platform reads to draw the
capability and to connect an org to it. A `block` manifest that holds one is
refused by `contract.CheckManifest`. This manifest is `sendcloud`, whose only
operation is `verify-connection`, so it declares no resource:

```yaml
apiVersion: mitstack/v1beta
type: capability
metadata:
  id: sendcloud
  name: Sendcloud
  owner: mitstack
spec:
  version: 1.0.0
  description: Parcels an app ships through Sendcloud.
  presentation:
    vendor: Sendcloud
    category: Delivery
    mark:
      - rect: { x: 3.5, y: 9, width: 17, height: 11, rx: 1 }
      - path: { d: "M3.5 9L6 5h12l2.5 4" }
  connection:
    mechanism: stored-token
    tokenSchema:
      - { name: public_key, secret: true, description: "Sendcloud public key" }
      - { name: private_key, secret: true, description: "Sendcloud secret key" }
  lifecycle:
    endpoint: https://api.mitstack.dev/operators/capabilities/sendcloud/hook
    operations: [verify-connection]
    contract: kit/v1
```

| Section | What it says |
|---|---|
| `presentation` | The `vendor`, the `category` the Capabilities page groups it under, and an optional `mark`: a list of shapes, each a `path`, `circle`, `rect`, `line`, `polyline`, `polygon` or `ellipse` whose attributes are numbers, or for a `path` its `d` string. Each list item holds exactly one shape |
| `connection` | The `mechanism`, one of `oidc`, `oauth-app` and `stored-token`. A `stored-token` connection lists its `tokenSchema` fields, each with a `name`, `secret`, optional `description`, optional `allowedValues` and an optional `optionalGroup` that makes the field optional but given with its group. An `oidc` connection may set `audience`, an `oauth-app` connection `installHint` and `heldOnlyByGrant`, and any connection a `driver` and an `objectStorage` key pair |
| `kinds` | The resource kinds, in lowercase kebab case, that decide the capability's scope |
| `reach` | The `capability` whose connection reaches this one |
| `docs` | Where the connect guide lives in git: `source` with `git`, `ref` and `path`, an `index`, and optional `pages` and `links`. A block may declare it too |

A manifest that lists `verify-connection` lists nothing else, and holds a
`stored-token` connection, since only a stored credential is checked.
[Serving a capability](handler.md#checking-an-orgs-credential-with-verify-connection)
shows how the operator answers it. A capability creates no resources of its
own, so `spec.resources` may be absent, while a `block` that lists none is
refused.

## Keys the parser refuses

`contract.ParseManifest` reads strictly. It refuses any key the
`mitstack/v1beta` types do not name, naming the key's path, so a typo fails at
parse time instead of being ignored. It also refuses any other `apiVersion`.

The retired version, `mitstack/v1alpha`, had keys such as a requirement's
`optional` and `spec.dependsOn`. A v1beta manifest that carries them is
refused like any other unknown key. So is a top-level `kind`, a
`spec.layer`, a `spec.params`, and `kind`, `in`, `on` or `inputs` on an entry.
The parser judges the grammar of type ids only, so it needs no type snapshot.

A manifest declares no inputs of its own. An input is a setting a resource
type declares in the platform's catalogue, such as the `location` and
`serverType` of `hetzner.server`. When someone subscribes an app to your
block, the platform asks them for the inputs of each type the block creates,
and `GET https://api.mitstack.dev/v1/resource-types` lists each type with its
inputs. Your block reads a value under the input's name, as in
`d.Params["location"]`, or a secret one from `Desired.SecretInputs`. A
setting none of your types declares belongs in a type's file in the
catalogue, as a new input.

The kit ships the JSON Schema for `mitstack/v1beta` as
`contract/manifest.schema.json`, generated from the same Go types, and an
editor can validate a manifest against it. A kit test fails when the
committed file differs from what the types generate.

## Block ids

`metadata.id` is a community id or a private id. A community id is a bare
slug, such as `billing-db`, and mitstack assigns it. A private id sits under
its org's name, as in `@acme/billing-db`, so two orgs can each hold a private
block named `api`. Each part is a slug of 1 to 40 lowercase letters, digits
and dashes that starts and ends with a letter or digit.

Write a private id in quotes, as in `id: "@acme/billing-db"`, because YAML
does not allow `@` at the start of an unquoted value. blockverify's manifest
suite refuses an id that `contract.ParseBlockID` refuses.
`contract.ParseCapabilityID` reads a capability's id the same way, and
`contract.ParseResourceTypeID` reads a type id such as `aws.ebs-volume` or
`@acme/aws.special-bucket`.

## The type snapshot

Resource types live in mitstack's type catalogue, not in the kit. The type
snapshot is one numbered version of that catalogue as JSON. It lists each
type with the outputs it declares, whether it is stateful, and the facts the
checks need. The platform serves it to any member of your org at
`GET https://api.mitstack.dev/v1/resource-types/snapshots/latest`, with an org
API token sent as `Authorization: Bearer <token>`. You mint that token on the
API Tokens page of the mitstack console. A block embeds the entries for the
types its manifest names, as the example's `type-snapshot.json` does, so a
first block can start from that file and add its own types' entries.

`contract.ParseTypeSnapshot` reads a snapshot, and
`contract.CheckManifestTypes` refuses a manifest entry listing a type the
snapshot does not hold. A block passes the snapshot to `handler.New` as
`Config.Types`.

| Snapshot field | What it means |
|---|---|
| `outputs` | Each output a resource of the type reports, with `required` when every such resource must report it and `secret` when its value is secret, such as a role's ARN or a cluster's credential |
| `stateful` | A resource of the type can hold data, so replacing it loses the data |
| `parts` | The objects that exist only to serve one resource of the type, such as the push role of an ECR repository, each with its `type` and `count`, the most one resource holds |
| `endsWithSubscription` | The resource's absence cannot be observed, so it ends when the block's unsubscribe succeeds |
| `providerHoldsNothing` | The resource is a record its block hands out, such as a runner label, and no provider holds an object of it, so no lister lists one |

`endsWithSubscription` marks a type such as `github.scaffold-contribution`,
the starter files a block commits into the app's repository. They stay in the
repository's history after unsubscribe, so no observer can prove them gone.
`providerHoldsNothing` marks a type whose resource stands for no provider
object, such as a runner label an app writes into its workflow. The checks in
[Testing a block](testing.md) then want no listed object for it and an absent
answer when it is torn down.
`contract.ParseTypeSnapshot` refuses a part with no type, a part whose type
the snapshot does not hold, and a count below one.

```json
{"id": "aws.ec2-instance", "parts": [{"type": "aws.iam-role", "count": 2}, {"type": "aws.iam-instance-profile", "count": 1}, {"type": "aws.elastic-ip", "count": 1}]}
```

## Reading a manifest in Go

`contract.ParseManifest` returns a `contract.Manifest`, which holds the
`apiVersion` and a `*contract.BlockManifest`, the parsed document itself.
`m.ID()` and `m.Version()` read through it, and
`m.Block.Spec.Resources` holds each resource as a
`contract.ManifestResource`. Its `Types` holds the listed ids, and its `Type`
holds the one id of an entry that lists one. A test reads a manifest through
`kittest.ReadManifest`, as [Testing a block](testing.md) describes.
