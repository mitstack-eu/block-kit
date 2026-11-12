# Serving lifecycle calls with the handler

This page is for a block author wiring an operator with the kit's `handler`
package. It shows how you build a handler, what the handler checks before your
code runs, what it calls for each lifecycle call, how it answers errors and
retries, and what its answer carries. The README's diagram shows one call's
path in brief, and this page fills in each part. House terms such as resource
type and kind are defined in
[the workspace glossary](https://github.com/mitstack-eu/mitstack-workspace/blob/main/docs/glossary.md).

## Building a handler

`handler.New` takes a `handler.Config` and returns a `*handler.Handler`. That
is an `http.Handler` serving `POST /hook`, where the engine sends lifecycle
calls, and `/logs`, where the platform reads recent log lines, as
[Serving a subscription's logs](#serving-a-subscriptions-logs) describes. The
example block builds its config in `newConfig` and serves it from
`main`:

```go
cfg, err := newConfig(newHetzner(hetznerAPI), logs)
if err != nil {
	log.Fatal(err)
}
cfg.JWKSURL = os.Getenv("MITSTACK_JWKS_URL")
h, err := handler.New(cfg)
if err != nil {
	log.Fatal(err)
}
server := &http.Server{Addr: listenAddr, Handler: h, ReadHeaderTimeout: readHeaderTimeout}
```

| Field | Required | What it holds |
|---|---|---|
| `Resource` | Yes | Your block's code, a `handler.Resource` |
| `Manifest` | Yes | The bytes of your `manifest.yaml`, whose `metadata.id` the signed token in every call, the OBO token the next section describes, must name. Without it `handler.New` refuses a config that sets any observer, and `kittest.AssertBlock`, which blockverify requires, fails |
| `JWKSURL` | Yes | Where mitstack publishes its public keys, `https://api.mitstack.dev/.well-known/jwks.json` in production |
| `Observers` | One per declared resource | A `handler.Observer` for each resource your manifest declares, keyed by the resource's name |
| `Types` | Yes, for the checks | The type snapshot your manifest names its types from, which `kittest.AssertBlock` and the unsubscribe of an `endsWithSubscription` type read |
| `TypeObservers` | No | Observers keyed by type id, for a resource an earlier version of the block declared |
| `Logs` | No | The `*webhook.LogBuffer` that `GET /logs` serves, which answers 404 without one |
| `SubscriptionLogs` | When the manifest lists `logs` | The function that answers `POST /logs` with one subscription's lines |
| `PublicKey`, `Replay`, `Now` | No | Replacements a test supplies for the key lookup, the nonce store and the clock |

`handler.New` returns an error for a configuration it cannot serve safely: no
`Resource`, no `Manifest`, no `JWKSURL`, a manifest the parser refuses, a
declared resource with no observer, and an observer under a name the manifest
does not declare. A `Config` without a `Manifest` is refused naming
`Config.Manifest`, because the manifest's `metadata.id` is the block every
call's token must name.
It also refuses a `TypeObservers` key that is not a type id, and a nil
observer under any key. Nothing in `Config` turns a trust check off.

## What the handler checks before your code runs

Every lifecycle call carries two signatures from mitstack's platform key, so
the operator holds no shared secret. The checks run in this order, and each
failure answers 401 without reaching your code, unless the step names
another status:

1. The `X-Mitstack-Key-Id` header names the signing key, and
   `X-Mitstack-Signature-Ed25519` carries the Ed25519 signature of the body.
   The handler looks the key up in the JWKS at `Config.JWKSURL`. When it
   holds no key for that id and its last fetch of the JWKS failed, it
   answers 503 instead, which the engine retries.
2. The body's `createdAt` must lie within five minutes of the operator's
   clock (`webhook.ReplaySkew`), and its `nonce` must be one the operator has
   not seen in that window, so a captured call cannot be replayed.
3. The handler decodes the body with `contract.DecodeRequest`, which refuses
   a missing required field or an unknown `operation` with a 400 naming the
   field. The operations are `subscribe`, `upgrade`, `validate`,
   `unsubscribe`, `issue`, the last only for a capability, and `lease`, only for
   a block that lists it, as [Leasing a short-lived credential](#leasing-a-short-lived-credential)
   explains.
4. The body's `obo` field must hold an OBO token, the platform's signed
   statement of the block, org, app and subscription the call is for. Its
   block must be your manifest's `metadata.id`, and its org, app and
   subscription must be the request's `orgId`, `appId` and `subscriptionId`.

The fourth check refuses a call signed for one block and replayed to another,
and a token naming another subscription. An operator serving several
manifests builds one handler for each, so each route checks its calls against
its own block.

The handler fetches the JWKS again once `webhook.JWKSRefreshInterval`, five
minutes, has passed, so a key the platform removes stops verifying within
that time. A key id the handler does not hold causes at most one extra fetch
in each interval, so a caller without a valid signature cannot make the
operator fetch at will. A failed fetch leaves the keys already held in place
until the next interval. The 503 of step 1 covers only a key id the handler
does not hold, because a failed fetch says nothing about the call.

Each refusal before your code runs is a 4xx whose `error` and `statusDetail`
both read `refused:<class>: <detail>`, so the engine reads every 4xx as a
refusal. The class follows the status, as this table shows.

| Status | Class | When |
|---|---|---|
| 400 | `malformed-request` | The body cannot be read, is too large, or fails `contract.DecodeRequest` |
| 401 | `untrusted-request` | A signature, replay or OBO token check fails |
| 404 | `unknown-path` | The path is neither `/hook` nor `/logs`, or `/logs` on a handler with no `Config.Logs` |
| 405 | `method-not-allowed` | `/hook` is called with anything but POST, or `/logs` with anything but GET or, on a handler with a `Config.SubscriptionLogs`, POST |

## The request your code receives

`Ensure`, `Delete` and each observer receive a `contract.Desired`, built from
the decoded request. It embeds a `contract.Identity`, the fields that say which
subscription a call is for and where its resources live, so `d.SubscriptionID`
and `d.Instances` read the same on every call.

| Field | What it holds |
|---|---|
| `SubscriptionID`, `BlockID`, `BlockVersion`, `OrgID`, `AppID` | Which subscription, block version, org and app the call is for |
| `Instances` | Each connection's settings and endpoint, keyed by the connection's name, such as the AWS region a block works in |
| `Secrets` | Each connection's secrets, keyed by the connection's name, as in `d.Secrets["hetzner"]["api_token"]` |
| `SecretInputs`, `AppSecrets` | The values of the secret inputs the block's types declare, and every secret the app holds when the manifest sets `receivesAppSecrets` |
| `OwnSecrets` | The secrets this subscription's block returned as its own on earlier calls, under the names it chose, as in `d.OwnSecrets["gatewayCaKey"]`, which [What the answer carries](#what-the-answer-carries) explains |
| `Params` | The subscription's values for the inputs the block's types declare, keyed by input name, as in `d.Params["location"]` and as [Writing the manifest](manifest.md) explains |
| `Bindings`, `BindingsAll`, `RequirementTypes` | The outputs of the resources that met the block's requirements, and which type met each, as [Writing the manifest](manifest.md) explains |
| `RecordedResources` | What the engine recorded for each resource: its type, its identity and, on an unsubscribe, `absent` when the engine already knows it is gone |
| `Absences` | The platform's record that a bound resource is gone, as a `contract.Absence` under the same requirement key `Bindings` uses. Read it through `d.AttestedAbsent`, which the paragraph below describes |
| `PriorOutputs`, `CloudIdentity` | The outputs the last call recorded, and the short-lived AWS web identity of the org's cloud account, nil when it has none |
| `VerbAt` | The time an observer reads after, which the next section explains |
| `NeverActive` | `true` only when the engine has proved the subscription holds no user data |
| `Replacing` | `true` on a request that carries `"replacing": true`, the marker of a replacement, as [Snapshotting and restoring state](#snapshotting-and-restoring-state) explains |

The platform runs an observer of its own, outside your block, which asks a
provider whether a resource still exists. When it reads a resource absent, the
platform records that on an unsubscribe as a `contract.Absence`: `Observer` names
the platform's observer, `Resource` the resource type (such as `hetzner.server`) it read, `ReadAbsentAt`
when it read it absent, and `ProvesAbsence` is true when the platform stands
behind the reading. This observer differs from the observers your block
registers. `d.AttestedAbsent(subscriptionID)` returns `(Absence, bool)` for the
binding, in `Bindings` or `BindingsAll`, whose `subscriptionId` is
`subscriptionID`. The boolean is true only when `ProvesAbsence` is true, `Observer`
and `Resource` are non-empty and `ReadAbsentAt` is set, and a request without
`absences` always gives false. Never infer absence from anything else.

A connection is an account the org connected at a provider, such as a Hetzner
Cloud project. The secrets reach your code only for the one call they arrive
with, and the handler stores them nowhere, its retry cache included.

The engine sets `neverActive` only when the subscription never reached active
and the platform did not create it to take over another subscription's data.
The platform does that when it moves an app to another stack and restores a
snapshot into the new subscription. A block may replace a stateful resource,
one that holds data, only when `NeverActive` is `true`, and anything else,
including a request without the field, reads as `false`.

## What the handler calls for each call

Your block's code is a `handler.Resource`. It also implements
`handler.ResourceReporter`, and the kit's checks need it, because it reports
each resource's identity:

```go
type Resource interface {
	Ensure(ctx context.Context, d contract.Desired) error
	Delete(ctx context.Context, d contract.Desired) (outputs map[string]string, err error)
}

type ResourceReporter interface {
	EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error)
}

type Observer interface {
	ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error)
}
```

The handler calls `EnsureResources` in place of `Ensure`, so a block keeps
`Ensure` only to complete the interface, as the example does. It observes each
resource your manifest declares through that resource's observer. An observer
asks the provider what stands right now, and never answers from what an ensure
remembered. Its `contract.ResourceRef` carries the resource's name, the type it
was created as, the identity that addresses it, such as
`{"firewallId": "38"}`, and the call's `Desired`. Its `Unsubscribing` field is
true only in the observe that follows an unsubscribe's `Delete`, so an observer
whose answer depends on the verb can tell that pass apart.

When `EnsureResources` fails after it made some resources, return those
resources beside the error. The failed answer carries each one's type and
identity, so the platform records them and a later unsubscribe observes each by
its real identity.

| Call | What the handler does |
|---|---|
| `subscribe`, `upgrade` | Runs one `EnsureResources`, or `EnsureResourcesOwning` for a block that implements `handler.OwnSecretsReporter`, then observes each declared resource once, in manifest order, and stops at the first not yet `present` |
| `unsubscribe` | Runs one `Delete`, or `DeleteKeeping` for a block that implements `handler.KeptReporter`, then observes each declared resource once, in reverse order, stops at the first not yet `absent`, and adds the outputs `Delete` returned to the answer once all are absent |
| `validate` | Asks a block that implements `handler.Converger` whether its work still runs, and while it does or until a subscribe reports it, answers without observing; otherwise observes each declared resource once. For a block that implements `handler.ReplacementPlanner` it names the resources an upgrade would replace. An observation carrying a blocker finding answers a refusal of class `handler.ClassBlocker` |

The handler never waits inside a call. A resource short of the state the call
asked for answers `pending:true` on a subscribe or upgrade and `absent:false`
on an unsubscribe, with a detail such as
`resource "hetzner.firewall" observed absent, not yet present`. The engine's settle
check (its polling of a pending subscription until the state is reached) then
carries on. It sends `validate` until every resource reads present, and sends
the call again when a validate answers `redrive`, the flag a block sets once
work left in flight has finished. It stops when the block's settle budget, the
total time its manifest allows, runs out. A teardown answered `absent:false`
is sent again in the same way.

On an unsubscribe the handler skips a resource whose recorded state is
`absent`, such as a database whose host's own teardown was already verified,
and your `Delete` reads the same record to leave it alone. It also skips the
observation of a resource whose type `Config.Types` marks
`endsWithSubscription`, and counts it absent once `Delete` succeeds.

A block that lands files in an app's repository reports the files its
unsubscribe left because the app changed them. It implements
`handler.KeptReporter`, whose `DeleteKeeping` replaces `Delete` and returns,
beside the outputs, those kept files keyed by the declared resource that landed
them. The handler answers them on that resource's delete outcome, and the
platform reads them from `resources.<name>.delete.kept`:

```json
{"absent": true,
 "resources": {"github.scaffold-contribution": {"delete": {"kept": [
   {"repository": "acme/weather", "path": "CLAUDE.md", "reason": "changed"}]}}}}
```

A kept file never carries its content. The handler answers 400 when
`DeleteKeeping` names a resource the manifest does not declare, and a block
implementing only `Delete` is served as before. The code is `deleteBlock` in
`handler/block.go`, and `landing.Remove` returns the list in its `Result.Kept`.

A block whose teardown leaves something alone on purpose names it by
implementing `handler.TeardownReporter`. Once an unsubscribe or a validate
proves every resource absent, the handler calls its `TeardownDetail`. The
engine settles a teardown on a validate it sends after the unsubscribe
answer, so both answers carry the detail. A non-empty detail
answers as the absent answer's `statusDetail`, such as opentofu-operator's
list of objects under the app's AppId whose tags name another subscription.
The handler asks nothing while a resource still stands.

Each observer is handed the identity and type `EnsureResources` reported for
its resource. When nothing was reported, it gets what the engine recorded,
and failing that, no identity and the one type its manifest entry lists. A
reported resource the manifest does not declare, or of a type its entry does
not list, answers 400, and so does a name reported twice. The answer carries
each reported type and identity as that resource's `ensure` outcome, so the
engine records them even when the call stops pending before it observes that
resource.

A member of an entry marked many, named `<entry>/<key>` as
[Writing a block's manifest](manifest.md#requirements-resources-and-what-a-block-provides)
describes, is observed as a resource of its own through its entry's observer.
The members take their entry's place in manifest order, in name order among
themselves. A subscribe or upgrade observes each member `EnsureResources`
reported, and an unsubscribe or validate each member the engine recorded. A
validate naming one member asks the entry's observer about that member alone.

An observer that reads the identity at the provider returns it in
`Observation.Identity`, and the `observe` outcome carries that identity. This
serves an ensure that answers before its work finishes and reports none. An
observer that reports no identity leaves the one it was handed on the outcome.
The handler refuses an identity holding one of the request's secrets, as it
refuses such an output.

An observer that reads records the provider writes over time, such as
`observe.NodeReported` reading node reports, counts only records made after
`Desired.VerbAt`, so a record older than the call cannot settle it. After
`EnsureResources` or `Delete`, the handler sets `VerbAt` to its clock once the
call returned. On a `validate` it is the request's `settlesAt`, the time of the
call the validate settles, and otherwise the request's `createdAt`.

A block that implements `handler.ReplacementPlanner` names, on a `validate`,
the resources an upgrade would replace, and the answer carries them under
`replaces`. Its one method returns the manifest names of those resources, and
a name the manifest does not declare answers 400:

```go
type ReplacementPlanner interface {
	PlanReplacements(ctx context.Context, d contract.Desired) ([]string, error)
}
```

The engine replaces a stateful resource only with an explicit confirmation,
and this list is how it learns that one is needed.

The handler also sets `plannedReplacements` to true on that answer, for every
block that implements `handler.ReplacementPlanner`, including one whose list
comes back empty. You write no code for it. The engine needs it because an
empty `replaces` alone cannot tell an upgrade that replaces nothing from a
block that never looked.

Three answers leave the field out, and the engine reads each as "not planned":
the answer of a block without the interface, the answer of a block built on a
kit release older than the field, and the answer to a `validate` naming one
resource, which runs no planner.

An empty list from `PlanReplacements` therefore tells the engine that the
upgrade replaces nothing. When your planner cannot tell, for example because
another call of your block holds a lock on what it reads, return an error instead. The validate
then answers that error and carries no `plannedReplacements`.

A validate answer from a planning block that replaces nothing reads:

```json
{"outputs": null, "absent": false, "plannedReplacements": true,
 "resources": {"server": {"observe": {"state": "present", "type": "hetzner.server", "identity": {"serverId": "4711"}}}}}
```

A `validate` may name one resource in `resource`, `resourceIdentity` and
`resourceType`, and the handler observes only that one. When the name is one
an earlier version declared and the manifest no longer does, the handler hands
the request to the observer `Config.TypeObservers` holds for its type. A name
neither the manifest nor a type observer covers answers 400, and so does a
`subscribe`, `upgrade` or `unsubscribe` naming one resource.

The engine sets `resourceUnsubscribing` when it probes a dropped resource
during a teardown. The handler then hands the resource's observer a
`ResourceRef` with `Unsubscribing` set, so the observer answers as it does
during an unsubscribe.

The handler checks the request's context before each resource. Once the
context is done, the call stops before that resource, names it in
`statusDetail`, and answers `pending:true`, or `absent:false` on an
unsubscribe.

A request ends when the platform stops waiting for the answer. The platform's
dry run, for example, gives each block's validate 5 seconds by default. The
platform then knows only that its deadline passed, so the handler writes one
line through Go's standard `log` package for a validate whose request ended. The
line names the request's subscription, how long the validate ran, and the
resource from your manifest it had reached:

```
validate: subscription sub-1: the request ended after 5.001s at resource "hostname"
```

The line reads `before any resource` when the request ended sooner, in
`PlanReplacements` or in the `Converging` check that
[work outlasting one call](#work-that-outlasts-one-call) describes. It carries
no output or identity value, and `logEnded` in `handler/declared.go` scrubs the
request's secrets from it.

## Errors, refusals and panics

The handler answers an error your code returns by its type. A
`*handler.Refused`, which you build with `handler.Refuse(class, format,
args...)`, answers 400 with a `statusDetail` of `refused:<class>: <detail>`.
The engine reads a refusal as permanent and stops retrying. Any other error
answers 500, and the engine retries the call under the same idempotency key.

A `*handler.Waiting`, which you build with `handler.Wait(format, args...)`,
holds a call back until something outside the block passes, such as a
provider's request limit. A `subscribe` or `upgrade` whose `EnsureResources`,
or `Ensure` for a block without it, returns one answers 200 with
`pending:true`. An `unsubscribe` whose `Delete` returns one answers 200 with
`absent:false`. Either answer carries the detail as `statusDetail`, which the
call's `progress` event repeats, as [Lifecycle events](#lifecycle-events)
describes. The handler observes nothing on that call. The engine asks again,
as it does for any pending answer, and the wait counts against the block's
settle budget.

`handler.WaitForSignal(format, args...)` builds the same wait for a call that
changed nothing because it waits on what another subscription reports, such as
a Docker host that has sent no node report yet. A node report is the state a
host pushes to mitstack on its own. The answer carries `redrive:true` beside
`pending:true`, which asks the engine to send the `subscribe` again. For an
example app named `shop`, the answer reads
`{"pending": true, "redrive": true, "statusDetail": "the host has sent no node report giving its allocatable capacity yet, so shop waits for that capacity report before it is sized"}`.

This wait is an exception to the settle check described above, which
polls a pending answer with `validate`. The engine handles it on a
`subscribe` in these steps:

1. The engine sends no `validate`, since nothing landed for one to observe.
   It sends nothing more for this subscription until step 2 or step 3 happens.
2. When the host's next node report reaches mitstack, the engine sends the
   `subscribe` again. Your block registers nothing for this, because the
   engine itself links the report to each subscription bound to the host.
3. If no report has ended the wait when the settle budget runs out, the
   subscription fails, naming the wait. The settle budget is the time the
   block's manifest, or else the engine's default, gives a subscription to
   finish.

On an `upgrade`, the handler still answers `pending:true` with `redrive:true`,
and the engine ignores the `redrive`. Every pending upgrade, waiting or not,
is followed by a `subscribe` before any `validate`, so that subscribe's own
answer starts the steps above.

On an `unsubscribe`, a `Delete` that returns this wait answers `absent:false`
with no `redrive`, as a plain wait does.

Your `EnsureResources` runs again from the start on each `subscribe`, so it
must write nothing before it returns this wait.

`handler.WaitOnRemoval(format, args...)` builds the same wait for a `Delete`
whose removal has started and now moves outside the block, such as a pull
request waiting on its checks. The `unsubscribe` still answers `absent:false`
with the detail. The answer also carries the observe of each resource the
engine has not recorded absent and whose type does not end with its
subscription, made with `Unsubscribing` set, so an observer
can report the removal under way, as
[A resource being removed](observe.md#a-resource-being-removed) describes. An
observe that fails is recorded as that resource's error, and the answer still
waits. `landing.Remove` returns this wait while its pull request waits on a
required check.

When `Delete` returns any other error, the handler answers that error at once and
observes nothing. Whatever `Delete` already removed stays removed, and the
engine's retry calls `Delete` again. `Delete` must therefore be idempotent: a
second call after the resource is gone reports it gone instead of failing.

A block whose resource sits on a host that cannot run its workload, such as a
server whose Docker does not answer, refuses with the class
`handler.ClassHostCannotRun`. The class tells the platform that a fresh
subscribe on the same host cannot repair the resource.

A validate whose check reads something only a created app has, such as a record
keyed by the app's id, answers `handler.NeedsApp` when the request names no
app. Its class is `handler.ClassNeedsApp`. The platform counts it as checked at
create for an app not yet created, and as a refusal once the app exists, as
the README's "A check that needs a created app" describes.
`handler.NeedsAppName` gives that answer for a request naming neither an app
nor `handler.ParamAppName`, and nil for any other.

A validate that cannot run because a requirement the block declares is not bound
in the request answers `handler.UnboundRequirement`, of class
`handler.ClassUnboundRequirement`. The platform counts it as checked at create
when only a sibling member not yet provisioned meets that requirement, and as a
refusal otherwise. `handler.RequireBound` gives that answer when the request's
`Bindings` and `BindingsAll` hold nothing for a requirement's reference. A
requirement bound to a resource that publishes no outputs also leaves them
empty, so call it only for a requirement the validate reads outputs of.

A panic in your code answers 500 with a fixed error naming the call, such as
`handler: the block panicked during subscribe`, and the answer never carries
the panic value. The handler logs the panic with the block, subscription and
call, after scrubbing every request secret from a string or error value. It
logs any other panic value only as its type, because printing it could show a
secret in a form a scrub cannot find.

## Retries and the idempotency cache

The engine sets `idempotencyKey` once per call and keeps it across every retry
of that call. The handler keeps a settled answer for an hour, keyed by
subscription and idempotency key, and replays it to a later delivery of the
same key. A delivery that arrives while the same key is still running waits
for it and receives the same answer. A `snapshot` or a `restore`, which
[Snapshotting and restoring state](#snapshotting-and-restoring-state)
explains, is also keyed by its unit, the `spec.continuity` entry it names. Two
units sent under one key therefore each reach your code.

The cache stores only a settled answer: a 2xx or a 400 refusal. It never
stores a 500, so a retry reaches your code again. It never stores a
`pending:true` answer or an unsubscribe's `absent:false`, because the engine
asks again to learn when the resources settle. A `validate`, an `issue` and a
request with no idempotency key bypass the cache, since a replayed probe could
report a state the resource has left.

## Lifecycle events

The request's events callback is a URL and token the platform includes so the
operator can report back. A `subscribe`, `upgrade` or `unsubscribe` sends
`progress` with its status detail while it is pending or still present,
`completed` once it settles, and `failed` on an error answer. A `snapshot` or
a `restore` sends `completed` on a 2xx answer, and `failed` on a refusal or an
error. A `validate` and an `issue` send none. An unsubscribe therefore completes only
after its teardown is observed absent.

## Work that outlasts one call

Some provider work runs longer than one call can wait, such as a builder that
images a disk for half an hour. Your `EnsureResources` starts the work and
returns, and the subscribe answers `pending:true`. The engine then polls with
`validate` until the work settles or the settle budget runs out.

While the work runs, the resource's observer reports `contract.Unknown`. Once
the provider shows the work done, such as the builder having powered itself
off, the observation also sets `Redrive`, and the validate answers:

```json
{"outputs": null, "pending": true, "statusDetail": "resources observed \"image-builder\" unknown", "redrive": true,
 "resources": {"image-builder": {"observe": {"state": "unknown", "type": "hetzner.server"}}}}
```

The engine then sends a fresh subscribe, which reaches your `EnsureResources`
again because the cache never stores a pending answer. `EnsureResources` takes
the next piece of work from what it observes at the provider, and running it
against finished work changes nothing. Report `Redrive` from the provider's
state alone, never from a clock or a count of calls. The handler reads it only
beside `Unknown`. A subscribe's own pending answer carries `redrive` only when
the block returns `handler.WaitForSignal`.

A resource the kit observer reads has no observer of yours to report this. Give
your `Resource` a method `Converging(ctx, d)` instead, which makes it a
`handler.Converger`. The handler asks it at the start of every validate. While
your work runs, return a detail such as `converging: the apply is running`, and
the validate answers every resource unknown with that detail and reads no
provider. Once the work has finished and no subscribe has reported what it
made, return `finished`, and the validate answers `redrive` beside `pending`.
The subscribe that follows reports the identities the work produced, which the
kit observer needs before it can read a resource. Return an error once the work
failed, and the validate answers it as the block's error, with no resource
outcomes. While nothing is in flight, return an empty detail, false and no error,
and the validate observes as usual. An OpenTofu module's apply is
such work, since the bucket it makes is named only once the apply has run.

## What the answer carries

An answer's `resources` map holds, for each resource the call reached, the
outcome of each of its calls, `ensure`, `observe` or `delete`, with the state,
outputs, identity and type observed. Each entry's `error` and `statusDetail`
are scrubbed of the request's secrets. A validate request and its answer look
like this:

```json
{"operation": "validate", "settlesAt": "2026-10-07T12:00:00Z",
 "recordedResources": {"aws.ebs-volume": {"type": "aws.ebs-volume", "identity": {"volumeId": "vol-0abc"}}}}
```

```json
{"outputs": null, "absent": false, "replaces": ["aws.ebs-volume"], "plannedReplacements": true,
 "resources": {"aws.ebs-volume": {"observe": {"state": "present", "type": "aws.ebs-volume", "identity": {"volumeId": "vol-0abc"}}}}}
```

The answer's top-level `outputs` and `secretOutputs` merge the outputs of the
resources `spec.provides` names, and a later resource wins a key two share.
An observer returns a secret value, such as a cluster's credential, in
`Observation.SecretOutputs`, because the type snapshot marks that output
secret. No secret output appears in `resources`, which the engine records. The
answer also carries `resourceSecretOutputs`, each provided resource's own
secret outputs under its name, so two `aws.iam-role` resources, whose type
marks `roleArn` secret, keep both ARNs:

```json
{"resourceSecretOutputs": {
  "deploy-role": {"roleArn": "arn:aws:iam::111111111111:role/deploy"},
  "ecr-push-role": {"roleArn": "arn:aws:iam::111111111111:role/ecr-push"}}}
```

A pending answer still carries the secret outputs of each provided resource
already observed present, and the platform stores them once the remaining
resources are confirmed.

A block keeps a secret for its own later calls, such as the key of a
certificate authority it made, as an own secret. Any observer returns one in
`Observation.OwnSecrets`:

```go
return contract.Observation{State: contract.Present,
	OwnSecrets: map[string]string{"gatewayCaKey": keyPEM}}, nil
```

A block whose ensure makes the secret, such as a key it mints on the first
subscribe, implements `handler.OwnSecretsReporter`, and the handler calls its
`EnsureResourcesOwning` in place of `EnsureResources`:

```go
type OwnSecretsReporter interface {
	EnsureResourcesOwning(ctx context.Context, d contract.Desired) (created []contract.CreatedResource, ownSecrets map[string]string, err error)
}
```

The answer to a subscribe or an upgrade carries under `ownSecrets` the own
secrets of its ensure and of every observation, and a validate's answer those
of every observation, pending or not. An ensure's own secrets reach the answer
even beside `handler.Wait`. The later step wins a name two return: an observer
overrides the value your ensure returned, and a later resource overrides an
earlier one. A refused or failed answer carries none, because the platform
keeps nothing from it, and so does a validate a `handler.Converger` answers
without observing. The platform keeps the set for the subscription and sends
it back on each later request of that subscription:

```json
{"operation": "upgrade", "subscriptionId": "sub_a1b2", "ownSecrets": {"gatewayCaKey": "<the key's PEM>"}}
```

Your code reads it as `d.OwnSecrets["gatewayCaKey"]`. A name an answer leaves
out keeps its value, so a block returns a key once and may omit it afterwards.
No request of another subscription carries the set, and neither the app's
secrets nor its secret listing holds it. The whole set, encoded as JSON, is at
most 8 KiB. The kit does not check that, but the platform fails a call whose
answer would take the set past it. Unsubscribe deletes the set. The handler scrubs each own secret from
every error and log line, as it does each other secret a request carries.

The handler refuses to report an output whose value equals one of the
request's `secrets` values. It checks every output an observer reports, every
identity `EnsureResources` reports and the outputs `Delete` returns. The
refusal is a `*handler.Refused` of class `secret-in-output` whose message names
the output's key and never its value, so the refusal cannot leak it. The check
compares whole values only, so an output that merely contains a secret passes
it, and your code must keep secrets out of plain outputs. The handler never
inspects `SecretOutputs`, where a secret belongs, and it ignores an empty
secret value, which names an unset optional secret.

## Serving a capability and issuing an app's credential

A manifest whose `type` is `capability` describes a service that serves more
than one app, such as a shared cluster or an org's payment provider. This
sense of capability is a manifest type, and it differs from a capability
folder such as `capabilities/aws`, which groups helpers by provider. The
handler serves a capability's four lifecycle calls as it serves a block's.

A capability may also give each app a credential narrowed to that app, such as
a one-hour access token. Its code then implements `handler.CredentialIssuer`:

```go
type CredentialIssuer interface {
	IssueCredential(ctx context.Context, d contract.Desired, appID string) (contract.Credential, error)
}
```

The platform asks for one app's credential with the `issue` operation, a
signed request to `/hook` that names the app under `issueAppId`. The handler
calls `IssueCredential` and answers 200 with the credential under
`credential`, as in
`{"outputs":null,"credential":{"values":{"accessToken":"tok-1"},"expiresAt":"2026-10-14T13:00:00Z"}}`.
It answers every issue afresh, because a replayed credential may have expired,
and it sends no lifecycle event for one.

| Case | Answer |
|---|---|
| The manifest's type is `block` | 400, refusal class `not-a-capability` |
| The capability implements no `CredentialIssuer` | 400, refusal class `issues-no-credential` |
| The request names no app under `issueAppId` | 400 |
| The credential holds no value or has no expiry | 500 |
| A credential value equals a secret the request carried | 500, naming the key and never the value |

The last row compares the credential with every secret the request carried:
each connection secret, each secret input, each app secret, each own secret
and the call's tokens. It compares whole values only, so a value derived from a secret
passes, and the capability's own code keeps such a credential from an app.

## Checking an org's credential with verify-connection

When a person connects their org to a capability, the platform asks the
capability's operator whether the credential they typed works, before any
subscription exists. The `verify-connection` operation carries the capability's
slug under `capabilitySlug`, the connecting org under `orgId` and the entered
fields under `enteredCredential`, keyed by the manifest's token field names.
It carries no subscription, no block id and no OBO token, and the request is
signed like every other call.

An operator whose manifest lists `verify-connection` as its only operation
sets `Config.Verifier` and no `Config.Resource`. The verifier answers with the
vendor's verdict:

```go
type ConnectionVerifier interface {
	VerifyConnection(ctx context.Context, c handler.ConnectionCheck) (handler.Verification, error)
}
```

The handler answers 200 with `verdict` set to `verified`, `rejected` or
`unreachable`, and `statusDetail` set to the verifier's `Detail` with every
entered value scrubbed out. A verifier reports `unreachable` when the vendor
said nothing about the credential, as on a transport error or an unexpected
status, and returns an error only for a fault in the operator itself.

| Case | Answer |
|---|---|
| The request names another capability than the manifest's id | 400, refusal class `wrong-capability` |
| The manifest does not list `verify-connection` | 400, refusal class `no-connection-check` |
| A verify-only operator receives any other verb | 400, refusal class `undeclared-operation` |
| The verifier answers a verdict other than the three | 500 |

The handler never replays a `verify-connection` from the idempotency cache,
because the credential may have been revoked since. The vendor call goes
through [`guardedhttp`](helpers.md#calling-a-vendor-with-an-orgs-credential).

## Leasing a short-lived credential

A block, or a Docker host (the server an `hcloud-docker-host` subscription
runs), sometimes needs a credential that only the owner of a resource can mint,
such as a signed upload link for a backup bucket or a write-only token for a
telemetry backend. This section calls the block or host that needs the
credential the caller. It calls the subscription whose operator manages the
resource the provider. A subscription is one customer's running copy of a
block.

The caller asks the platform, which a program on a node does through
`helpers/lease`, as
[Asking the platform for a lease](helpers.md#asking-the-platform-for-a-lease)
describes. The platform asks the provider's operator for
the credential with the `lease` operation, a signed call to the operator's
`/hook` like any other lifecycle call. The call goes to the provider, so the
request's `subscriptionId`, and the subscription its OBO token names, are the
provider's, and the caller appears only in `Caller`. The platform never needs
to know how a provider signs a credential, so the provider's key format stays in
the provider's block. A block that provides a resource and mints these
credentials lists `lease` under `spec.lifecycle.operations` and implements
`handler.Leaser`. The platform sends the operation only to a block that lists
it:

```yaml
lifecycle:
  endpoint: https://hcloud-docker-host.example.com/hook
  operations: [subscribe, unsubscribe, upgrade, validate, lease]
  contract: kit/v1
```

```go
type Leaser interface {
	Lease(ctx context.Context, d contract.Desired, l contract.LeaseRequest) (contract.Lease, error)
}
```

`handler.New` refuses a manifest that lists `lease` when `Config.Resource` is
not a `Leaser`, so a lease never reaches a block that cannot answer it.
`Desired` carries the provider's own connection secrets, which the block signs
with. `contract.LeaseRequest` holds what the caller asked, and `contract.Lease`
is what the block answers:

```go
type LeaseRequest struct {
	ID       string          // the lease id
	Resource string          // the resource to lease from
	Want     json.RawMessage // a JSON object the platform never reads, or nil
	Retire   []string        // handles of earlier credentials to delete
	Caller   LeaseCaller
}

type LeaseCaller struct {
	SubscriptionID string
	Placed         []Placed
}

type Placed struct {
	App            string
	BlockID        string
	SubscriptionID string
}

type Lease struct {
	Credential json.RawMessage // a non-empty JSON object, passed on unread
	ExpiresAt  time.Time
	ID         string // the block's handle for deleting the credential, empty if none is needed
}
```

| Field | Meaning |
|---|---|
| `ID` | The lease id, which the platform chooses and keeps the same on every attempt of one lease. It is empty on a request that only retires |
| `Resource` | The reference the caller's manifest uses for the resource: its exact type, such as `hetzner.backup-bucket`, or its name where two resources share a type. The block answers for the resource of its own that this reference names |
| `Want` | An object the platform forwards and never reads, such as `{"signals":"metrics,logs"}` |
| `Retire` | The handles of earlier credentials to delete. Each is a `Lease.ID` this block answered before |
| `Caller` | The calling subscription in `Caller.SubscriptionID`, and in `Caller.Placed` the apps and blocks the platform records as running on the caller |

A `Placed` entry looks like
`{"app":"shop","blockId":"docker-mariadb","subscriptionId":"sub_db"}`, one for
each block of an app that the platform records as running on the caller. The
platform builds the list from its own records and not from the caller's
request, so the block can trust it. A block mints only for what `Caller.Placed`
lists, and a caller cannot name an app in `Want`, which keeps a host to its own
apps. A block reads `Want` for the shape of what it mints, such as the signals
of a token, and never for which apps.

The platform sends `Retire` when a credential that has a handle is no longer
wanted. Its expiry may have passed with no newer lease, or the caller may have
been unsubscribed. One request can both mint and retire, in which case the
block deletes the listed handles and then mints. A request with an empty lease
id only retires: the block deletes what `Retire` lists, and the handler answers
200 with no `lease`.

The handler answers 200 with the credential under `lease`, as in
`{"outputs":null,"lease":{"credential":{"token":"..."},"expiresAt":"2026-10-14T13:00:00Z","id":"tok_1"}}`.
The platform passes `credential` on unread and records only `id` and
`expiresAt`, and it records nothing when `id` is left out, as for a link that
expires on its own.

| Case | Answer |
|---|---|
| The manifest does not list `lease` | 400, refusal class `lease-not-listed`, and the block is never called |
| The request names no resource, or mints without a caller, or neither mints nor retires | 400 |
| The lease holds no credential, a credential that is not a non-empty JSON object, or one whose strings are all empty | 500 |
| The lease has no expiry, an expiry already passed, or one more than `contract.MaxLeaseLife`, 24 hours, ahead | 500 |
| A string in the credential, or the `id`, equals a secret the request carried | 500, naming where in the credential it sits, such as `credential.nested.key`, and never the value |

A 500 carries no `lease`, so the platform records nothing for it. When the
handler refuses a lease whose answer carried a handle (the `ID` of the
`Lease` the block returned), it calls the block once more to retire that
handle, since the platform records only what a 200 carries and could never find
the credential. A retire that fails joins its error to the refusal, and a
refusal the retire itself returns never changes the status of the refusal
being answered.

The last row compares whole values only, so a signed link that names the key
id that signed it passes. The handler never replays a lease from the idempotency
cache, because a replayed credential may have expired, and it sends no
lifecycle event for one.

## Running a one-off command

A builder sometimes needs one admin step inside their app, such as adding a
user, and nobody logs in to a host mitstack manages. The platform asks the
block that runs the app with the `run` operation, a signed call to the
operator's `/hook` like any other lifecycle call. The block starts the command
in a fresh container of the app's image and answers once the command has ended.
[ADR 0142](https://github.com/mitstack-eu/mitstack/blob/main/docs/adr/0142-a-one-off-command-runs-in-the-apps-container.md)
holds the reasoning.

A block that answers it lists `run` under `spec.lifecycle.operations` and names
its function in `Config.Run`. The platform sends the operation only to a block
that lists it:

```yaml
lifecycle:
  endpoint: https://app-slice.example.com/hook
  operations: [subscribe, unsubscribe, upgrade, validate, logs, run]
  contract: kit/v1
```

```go
cfg.Run = dockerhost.Runner{Block: "docker-app-slice", Processes: []string{"web"}, Reports: observe.NodeReportsClient{}}.Run
```

`handler.New` refuses a manifest that lists `run` with no `Config.Run`, and a
`Config.Run` whose manifest does not list `run`, so a run never reaches a
block that cannot answer it. blockverify fails such a block and quotes that
refusal in its `resources` suite. A block whose app runs on a Docker host uses
`dockerhost.Runner`, as above and in
[Running a command in an app's container](helpers.md#running-a-command-in-an-apps-container).

The request carries three fields beside the usual ones, and `req.Run()` hands
them to the function as a `contract.RunRequest`:

| Field | Go field | Meaning |
|---|---|---|
| `runId` | `ID` | The id the platform mints when it accepts the run, `run_` and twelve lowercase hex characters, kept the same on every send of one run |
| `command` | `Command` | One line of at most 1,000 printable ASCII characters, `contract.MaxRunCommandLength`, which the container runs with `sh -c` |
| `process` | `Process` | The container of the part the command runs in, such as `web`. It is empty when the builder named none, and the block then runs its entry, the process that serves the app's address |

A run request looks like this, cut to the fields a run adds:

```json
{
  "operation": "run",
  "subscriptionId": "sub_a1b2c3d4e5",
  "runId": "run_4c1e9a7f2b3d",
  "command": "php bin/add-user.php alice@example.com",
  "process": "web"
}
```

`contract.DecodeRequest` refuses a run whose id has another form, whose command
is empty, longer than 1,000 characters, of a second line or holding a
character outside printable ASCII, and whose process is not lowercase letters,
digits, hyphens and underscores. A refused request never reaches the block.
`contract/goldens/run.json` is the whole request the platform sends.

The engine sends the same run again on each node report of the host until the
block answers that it has ended. Each answer of the function maps to one answer of the
handler:

| The function returns | The handler answers |
|---|---|
| A `contract.RunResult` | 200 with the result under `run`, every request secret scrubbed from its output |
| An error wrapping `handler.Wait` | 200 `pending:true`, with the wait's detail as `statusDetail` |
| A `handler.Refuse` | 400 with the refusal, as when the host refused the run or the run names a process the block does not run |
| Any other error, or a panic | 500 |

An answer that ended looks like this:

```json
{"outputs":null,"run":{"exitCode":0,"output":"added alice@example.com\n","digest":"sha256:5f1c","startedAt":"2026-11-11T14:00:09Z","endedAt":"2026-11-11T14:00:10Z"}}
```

`exitCode` and `output` are always there, and `digest`, `startedAt` and
`endedAt` name the image and the times when the host reported them. A command
that exits non-zero still ended, so its answer is a 200 with that exit code.
A block whose manifest does not list `run` answers 400 with the refusal class
`run-not-listed`. The handler never replays a run from the idempotency cache,
because each send reads the host's newest report, and it sends no lifecycle
event for one, since the platform records the run itself.

## Snapshotting and restoring state

A block that keeps data carries it across a replacement with two operations,
`snapshot` and `restore`. A replacement is an upgrade the user confirms in
which the block creates a resource that holds data again, such as a
`hetzner.server`, on the same subscription. This section is for a block writer
whose manifest declares state under `spec.continuity`, as
[The state a block keeps](manifest.md#the-state-a-block-keeps) explains. A
unit is one entry of that declaration. A handover is the string a snapshot
answers, which names where the snapshot is so that the restore can find it.

[ADR 0125](https://github.com/mitstack-eu/mitstack/blob/main/docs/adr/0125-a-confirmed-upgrade-replaces-a-stateful-server-through-a-recorded-handover-with-keys-from-a-per-subscription-escrow-and-two-drills-that-keep-it-working.md)
names four stages of a replacement: snapshot, reinstall, restore and observe.
This is what a block does at each:

1. Snapshot: the block copies a unit's data out of the old resource. The kit's
   `snapshot` operation serves this stage, with one unit per request.
2. Reinstall: the block creates the new resource. ADR 0125 makes a replacement
   an ordinary upgrade the user confirms, and an upgrade reaches your
   `EnsureResources`.
3. Restore: the block puts the unit's data back. The kit's `restore` operation
   serves this stage, with one unit per request.
4. Observe: the block's observers report each replaced resource. ADR 0125
   ends the replacement when each reads present and healthy, which an
   observation shows as `Present` with no `Unhealthy` cause.

A block lists both operations under `spec.lifecycle.operations` and implements
`handler.Snapshotter`, and `handler.New` refuses a manifest that lists them
over a `Resource` that does not:

```go
type Snapshotter interface {
	Snapshot(ctx context.Context, d contract.Desired, unit string) (handover string, err error)
	Restore(ctx context.Context, d contract.Desired, unit, handover string) error
}
```

Both calls name one unit in the request's `unit`. The handler refuses a unit
whose manifest entry does not say `handover: snapshot`, before the block runs.

Your `Snapshot` copies the unit's data to a place outside the resource that is
about to be replaced. It then restores that copy into a throwaway place, such
as a temporary database or directory, and checks it there, for example that a
known query answers. It returns the handover when the check passes and an
error when it does not. That check is the block's own work: the handler
publishes whatever handover the block returns without an error, and answers a
snapshot that returns an error with that error and no handover.

The handover is a string your block chooses. The handler publishes it as the
answer's output `contract.HandoverOutputKey(unit)`, which is `handover.`
followed by the unit's name. On a restore it reads the string the request
carries in `priorOutputs` under the same key, and passes it to `Restore`
unchanged. The string names where the snapshot is, such as an object's
reference with its digest, so that your `Restore` alone can find the data
again. It holds no secret, since it travels as an output.

This request asks for a snapshot of the unit `app-data`, cut to the fields a
snapshot adds, and the answer carries the handover:

```json
{"operation": "snapshot", "subscriptionId": "sub_a1b2c3d4e5", "unit": "app-data", "replacing": true}
```

```json
{"outputs": {"handover.app-data": "hnd_0001"}}
```

`"replacing": true` on a request tells the block that the request belongs to
a replacement, and the block reads it as `d.Replacing`. The marker is for a
block whose `EnsureResources` starts a server's install a second time when the
first attempt has not finished. Such a block skips that second install while
`d.Replacing` is true, since ADR 0125 rules out an install retry during a
replacement.

| What goes wrong | Status |
|---|---|
| The manifest does not list `snapshot` and `restore` | 400, `refused:snapshot-not-listed` |
| The unit's manifest entry does not say `handover: snapshot`, or the manifest has no such unit | 400, `refused:unknown-unit` |
| A restore carries no handover under the unit's output key | 400, `refused:no-handover` |
| The block returns a `handler.Refuse` | 400, with the block's own refusal class and no handover |
| The block returns any other error, or a handover that is empty or repeats a secret the request carried | 500, with no handover |

The kit's shared hooks take the snapshot, check it and restore it for a volume
and an OpenBao store, as [The continuity hooks](helpers.md#the-continuity-hooks)
explains.

## Serving a subscription's logs

The console's Troubleshoot panel and MCP's `get_app_logs` show a part's recent
lines. The platform reads them from the block in one of two ways, and the
block's manifest decides which.

A block that keeps one operator-wide buffer sets `Config.Logs`. The platform
reads it with a bodyless signed `GET /logs`, which names no subscription, so
every subscription sees the same lines. One operator serves the subscriptions
of every org, so those lines can name another org's subscriptions.

A block that serves each subscription's own lines lists `logs` under
`spec.lifecycle.operations` and names a function in `Config.SubscriptionLogs`:

```yaml
lifecycle:
  endpoint: https://app-slice.example.com/hook
  operations: [subscribe, unsubscribe, upgrade, validate, logs]
  contract: kit/v1
```

```go
cfg.SubscriptionLogs = func(ctx context.Context, d contract.Desired) ([]string, error) {
	host := d.Bindings["docker-host"]
	return dockerhost.SubscriptionLogs(ctx, observe.NodeReportsClient{}, d, host["subscriptionId"])
}
```

A block that runs an app's containers on a Docker host, a server an
hcloud-docker-host subscription runs, uses `dockerhost.SubscriptionLogs`, as
above. It reads the host's newest node report, the signed status the host
sends mitstack on a timer and on each container change, and keeps the last release command's output and the lines of each container
whose `mitstack.dev/subscription` label is the request's subscription, each
led by its container's name:

```text
app-shop-release | the release command exited 1 on ghcr.io/acme/shop@sha256:ab12
shop-web | GET / 500
```

When it has no lines, it answers one line saying why: the host has sent no
report yet, the host's release sends no container output, or no container of
the subscription runs on the host yet. A block whose app runs anywhere other than a
Docker host writes its own function that reads its subscription's lines.

The platform then sends a signed `POST /logs` whose body is a request with the
operation `logs`. It carries what a hook carries for that subscription: its
params, connections, secrets and bindings, and the events callback through
which `ctx` reaches the platform's routes, such as the host's node reports.
The handler trusts it as it trusts a hook, checking the signature, the replay
window and the OBO token. It then calls your function with the request's
`contract.Desired` and answers `{"lines": [...]}`, with each secret the
request carried scrubbed from every line.

| Case | Answer |
|---|---|
| A signature, replay or OBO token check fails | 401, before your function runs |
| The request's operation is not `logs` | 400 |
| A `logs` request sent to `/hook` | 400 |
| Your function returns a `handler.Refuse` error | 400, carrying its class |
| Your function returns any other error, or panics | 500 |
| A `POST /logs` to a handler with no `Config.SubscriptionLogs` | 405 |

`handler.New` refuses a manifest that lists `logs` without a
`Config.SubscriptionLogs`, and a `Config.SubscriptionLogs` on a manifest that
does not list `logs`, since the platform would then never call it. A handler
may set both fields, and the `GET /logs` keeps answering from the buffer.
