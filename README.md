# operator-kit

This module is the contract every mitstack block imports to talk to the
engine. It covers the webhook trust boundary a block's handler sits behind,
the events a block emits as it works, and `kittest`, the conformance harness
a block's own tests run against the same fixtures the engine is tested
against. The `contract` package owns the wire contract itself: the envelope
and response JSON schemas, the golden request and response fixtures both
sides are checked against, and the resource registry. The registry is the one
list of output names a block may declare in `spec.outputs` and read under
`spec.requires[].reads`, and the engine reads it from this package.

The `contract` package also holds the resource-type catalogue, which is the
one list of resource types a block can create or reference.
`contract.ResourceTypes` returns every type, sorted by its ID, and
`contract.LookupResourceType` returns the one type with a given ID, such as
`aws.ecr-repository`. Each type carries a kind, which names what the
resource is without naming a provider: the kind of `aws.ecr-repository` is
`container-repository`. Each type also carries the capability it belongs to
and a category. It lists the languages it serves and the artifact type it
holds, if any. Its lifecycle says whether replacing the resource loses
data, and its scope says whether it serves one app or the whole
organisation. It says whether the resource is priced, and it names the
registry outputs the resource produces.

A type belongs to the capability that owns its lifecycle, which is the
capability that creates the resource and later deletes it. One thing reached
through several capabilities is therefore several types. An EC2 instance is
`aws.ec2-instance`, `cloudformation.ec2-instance` or
`crossplane.ec2-instance`, depending on which capability creates it, and all
three have the kind `host`. Types that share a name, the part of the ID after
the first dot, share a kind, and `contract.ValidateResourceTypes` refuses a
catalogue in which they do not.

The `contract` package also owns the grammar of block, capability and
resource-type ids, which the platform's ADR 0080 sets. A community name is
bare, with no prefix, and mitstack assigns it. A private name sits under its
org's namespace, as in `@acme/billing-db`. Two orgs can then each hold a
private block named `api`, and a private name never takes a community one,
because no bare name is private. Each part is a slug of 1 to 40 lowercase
letters, digits and dashes that starts and ends with a letter or digit.
`contract.ParseBlockID` and `contract.ParseCapabilityID` accept `billing-db`
or `@acme/billing-db`, and `contract.ParseResourceTypeID` accepts
`aws.ebs-volume` or `@acme/aws.special-bucket`.

blockverify, the kit's conformance runner, judges a manifest's `metadata.id`
with `contract.ParseBlockID` in its manifest suite. Write a private id in
quotes, as in `id: "@acme/billing-db"`, because YAML does not allow `@` at
the start of an unquoted value.

The types `contract.Step` and `contract.SuppliedResource` each carry an
`ownership` field. Its value is `created` for a resource the block creates
and deletes, or `referenced` for one the block only uses.

A manifest may list the resources a block creates as ordered steps under
`spec.steps`, as the platform's ADR 0079 sets. Such a manifest requires only
resources. Each `contract.ResourceRequirement` is matched by a kind and the
outputs it reads, or narrowed to one type, and a requirement with
`many: true` collects every offer in the stack. A block offers the resource
a step creates by listing `- step: <id>` under `spec.provides`, which
`contract.Offer` holds. `contract.ValidateBlockSteps` checks a block's steps,
requirements and offers against the catalogue, naming the step, requirement
or offer at fault. `contract.DeriveCapabilities` returns the capabilities
the block's step types and narrowed requirements belong to, so a manifest
never lists them by hand. blockverify's manifest suite runs the same check
on any manifest that declares `spec.steps`.

## Importing it

The module path is `github.com/mitstack-eu/operator-kit`. A block adds it the
usual way:

```
go get github.com/mitstack-eu/operator-kit@vX.Y.Z
```

The repository is private, so the fetch needs `GOPRIVATE=github.com/mitstack-eu`
in the environment and a credential able to read this repository (an SSH key
or a token configured for `github.com` in `~/.netrc` or `git config`).

## The typed contract

`contract.DecodeRequest` decodes a lifecycle webhook body into a
`contract.Request`, a Go struct whose JSON tags mirror envelope.schema.json
field for field. It also checks the schema's required set — operation,
nonce, createdAt, subscriptionId, blockId, orgId — and the operation enum,
so a malformed envelope fails with an error naming the JSON field before an
operator's own logic ever sees it, and when `cloudIdentity` or
`buildRequest` is present it checks their own required properties the same
way, naming them `cloudIdentity.provider` and similar. `contract.DecodeResponse`
does the same for a `contract.Response`, decoded against response.schema.json;
a field of the wrong type fails the same way, naming the field.

`Request.Identity()` returns a `contract.Identity`: the subset of a request
that names which subscription an operation targets and where its resource
lives — subscription, block, org, app, its resolved tool instances, its
prior outputs, and its cloud identity — independent of what it should
converge to. The instances belong here because a teardown has to address
the resource where it was provisioned: a block scoped to an AWS region
reads that region out of `Identity.Instances`, and `Delete` and `Observe`
are handed an `Identity` and nothing else by default — a Resource that opts
into `handler.RequestObserver` or `handler.HandoverResource` (below) sees
the verb's own `Desired`, secrets included, instead. `Request.Desired()`
returns a `contract.Desired`, which embeds `Identity` and adds the params, secrets
and bindings that describe what an operator should converge its resource
to, so `d.Instances` inside `Ensure` resolves through the embedded
`Identity` to the same map a teardown sees. Splitting a request this way
lets a validate/unsubscribe handler read only `Identity`, without pulling in
fields a teardown call never needs.

`contract.Observation` is what a validate or unsubscribe check found: a
`contract.State` (`Present`, `Absent`, or `Unknown`) plus whatever outputs
and status detail it gathered along the way. `Observation.Response()` turns
that into the `contract.Response` the webhook handler returns, mapping
`State` onto the response's `absent` pointer: `Present` sets it to `false`,
`Absent` sets it to `true`, and `Unknown` leaves it `nil` so the engine
fails open exactly as an operator that never implements the marker does.

## The Handler and Resource

A block implements `handler.Resource` — `Ensure`, `Observe`, `Delete` — and
`handler.New` wires it behind the trust boundary, the settle loop (a bounded
poll of `Observe` until the resource actually serves) and the
idempotency cache described below, calling whatever `Hooks` the block's
`Config` registers at `HookBeforeEnsure`, `HookAfterEnsure`, `HookBeforeDelete`
or `HookAfterDelete`.

A block declares its hooks in the manifest under `spec.lifecycle.hooks`, the
path held in the constant `contract.LifecycleHooksField`.
`contract.LifecycleHookPoints` is the closed set of names a block may declare
there. The handler and the platform's manifest validation both refuse a
name outside that set, and both refuse a name listed twice. Both sides test against
the same manifest golden, `contract.HooksManifestGolden`, so a change on
either side turns the other side's tests red. Its twin,
`contract.TypeBlockHooksManifestGolden`, is the same block written with
`type: block`, `format` and `store` and without a layer, and the handler
reads it exactly as it reads the first.

A request that carries no OBO token is refused with 401 on every verb —
subscribe, upgrade, validate, unsubscribe — before the Resource is ever
consulted. The token is the platform's own signed assertion of the
consuming org, app and subscription, and nothing in `Config` or the
manifest turns the check off.

An error the Resource or a hook returns is answered by its kind. A
`*handler.Refused` answers 400 with a `refused:<class>` status detail,
which the engine treats as permanent. Any other error answers 500, which
the engine retries under the same idempotency key, and the idempotency
cache never stores a 500, so the retry reaches the Resource again. A panic
in the Resource or a hook is answered the same way as that second kind: the
Handler recovers it and answers 500 with a fixed error naming the verb,
such as `handler: the block panicked during subscribe`. The response never
carries the panic value. The Handler recovers inside the idempotency cache's
single-flight call, so a delivery waiting on the same key receives that
same response rather than an empty one. The panic is logged with the block,
subscription and verb. A string or error panic value is logged with every
request secret scrubbed where it appears literally, and any other value is
logged only as its type, because formatting it could print a secret in a
form a literal scrub cannot find. A block cannot turn this recovery off.

The idempotency cache is keyed by subscription and by the request's
`idempotencyKey`, which the engine sets once per call and keeps the same
across every retry of that call. It stores only a settled answer, for an
hour: a settled 2xx or a 400 refusal is replayed to a later delivery of the
same key, and a delivery that overlaps one still in flight waits for it and
shares its answer. A 2xx that answers `pending:true` reaches the caller but
is never stored, because the engine re-drives the same key to learn when
the resource converges, and a stored pending answer would replay "still
pending" instead of observing it. An unsubscribe that answers
`absent:false` also reaches the caller but is never stored, because absence
must come from observing the resource, and the redelivery observes it
again. A subscribe or upgrade answer that carries `absent:false` is stored
as usual, since that is what a present resource answers. Two kinds of
request bypass the cache entirely: a validate, because it is a probe of the
resource's current state and a replayed observation could report a state
the resource has left, and any request whose `idempotencyKey` is empty.

A block whose resource sits behind a token — or otherwise needs the
request's own secrets to authenticate — implements `handler.RequestObserver`:

```go
type RequestObserver interface {
	ObserveWithRequest(ctx context.Context, d contract.Desired) (contract.Observation, error)
}
```

The Handler checks for it with a type assertion everywhere it would
otherwise call `Observe`: once on validate, and on every settle poll a
subscribe, upgrade or unsubscribe drives. A Resource that also implements
`RequestObserver` has `ObserveWithRequest` called instead, with that verb's
own `Desired` — secrets included. A Resource that does not implement it is
unaffected and keeps calling `Observe(ctx, Identity)` exactly as before. The
secrets reach `ObserveWithRequest` only for the duration of that one verb
call: the Handler stores them nowhere of its own, the idempotency cache
included, so a later, unrelated request never sees them.

`Delete` already has a route to the request's secrets, through
`DeleteWithRequest` below: `Request.Desired()` copies `Secrets` onto the
`Desired` it returns, the same way it copies `Params`, so a block that
implements `HandoverResource` sees the unsubscribe request's secrets there
too.

A block whose teardown needs the unsubscribe request's own params, or that
must return outputs from tearing down, also implements
`handler.HandoverResource`:

```go
type HandoverResource interface {
	DeleteWithRequest(ctx context.Context, d contract.Desired) (outputs map[string]string, err error)
}
```

The Handler checks for it with a type assertion before calling Delete. A
Resource that also implements HandoverResource has `DeleteWithRequest` called
instead, with the unsubscribe request's full `Desired` — Params included,
unlike the bare `Identity` Delete receives. A Resource that does not
implement it is unaffected and keeps calling `Delete(ctx, Identity)` exactly
as before. Returning an error from `DeleteWithRequest` aborts before
anything is destroyed: the Handler answers the error response immediately,
with no destroy step and no settle poll.

This is the shape the kit's own `handover` package exists for: a block reads
`d.Params["dataHandover"] == "snapshot"` to decide whether to snapshot data
before destroying it, and returns the resulting id under the key
`handover.OutputKey` ("dataSnapshotId") so it lands in the unsubscribe
response's `outputs`, alongside `absent:true`, once the resource settles
absent.

`DeleteWithRequest` must be idempotent. A settle timeout answers
`absent:false` with no outputs, and the platform's redrive calls
`DeleteWithRequest` again, possibly after an earlier call already destroyed
the resource; a second call must find the snapshot (or other handover
artifact) it already made, or report the resource gone, rather than fail.
Its outputs also reach the wire unscrubbed, the same way Observe's do, so
they must never carry secret material.

### Retaining a resource on unsubscribe

A block whose manifest declares `spec.lifecycle.retainsOnUnsubscribe: true`
is one that by design keeps the resource it created — a consumer's own app
repository, say — so a normal teardown's `Observe` poll would just keep
reporting `Present` forever, since nothing ever destroys the resource to
make it go absent. Such a block declares `retainsOnUnsubscribe` so the
Handler settles its unsubscribe without ever probing for absence: as soon as
the `beforeDelete` hook, `Delete` (or `DeleteWithRequest`) and the
`afterDelete` hook have all succeeded, it answers `absent:true` at once,
carrying any `HandoverResource` outputs, with a `statusDetail` explaining
that the block declares it retains its resource so absence was not
observed. `Observe` is never called for that unsubscribe.

A failed `Delete`, `DeleteWithRequest` or delete-side hook is unaffected by
the declaration: it still answers the ordinary error response, never
`absent:true`. A block that does not declare `retainsOnUnsubscribe` — the
field defaults to `false` — keeps the ordinary behaviour: the Handler polls
`Observe` up to `VerbBudget` times and answers `absent:false` if the budget
runs out still non-absent.

### The output-secret gate

Before any response leaves validate, a settled `Present` or a settled
`absent:true`, the Handler refuses one whose `Outputs` carry a value that
exactly matches one of the request's `secrets` values, over the merged
`Observe` and `HandoverResource` outputs. The refusal is a `*handler.Refused`
with `Class` `"secret-in-output"`, and its message names the offending
output key — never the value — so the refusal itself never becomes a second
leak.

The check is exact-value, not substring: an output that merely contains a
secret (an owner login embedded in a repository path, say) is left alone,
since scrubbing every occurrence would also mangle a legitimate output. This
is the gate's honest limit — it catches a secret returned verbatim as an
output value, and it cannot detect one embedded inside a longer value — so a
block must never put secret material in `Outputs`. Secret material belongs
in `SecretOutputs`, which the gate never inspects, since that field is where
a Resource is meant to return a secret on purpose. An empty secret value is
ignored: it names an unset optional secret rather than an actual one, and an
empty output value leaks nothing, so the two never combine into a false
refusal.

## Testing

```
go vet ./... && go test ./...
```

The module is stdlib-only by design: no third-party dependency has ever been
needed, and none is added.

## Release

Every push to main that changes the tree cuts the next patch tag
(`scripts/release-operator-kit.sh`, run from CI). A commit message carrying a
`kit-release: minor` or `kit-release: major` line raises that level instead,
and each raise zeroes the levels below it. A skipped or failed release run is
not lost: the next push resolves the base from the newest tag and cuts the
release the earlier push owed.

## Recording a delivery on the board

Every push to main also records which board issues it delivered, through
`cmd/board-delivery`. The `boardkeys` package derives the keys a commit
delivers: a key counts only when it sits in a leading key list in the
commit's subject, terminated by a colon, or in a whole-line `Jira:` trailer
in the body. A key anywhere else in a subject, or in an ordinary body line,
is prose and delivers nothing.

`board-delivery` reads a local `board-synced` git tag as the anchor for its
range: the commits since that tag, or since the push event's `before` sha
when no tag exists yet, or the single pushed commit when neither is usable.
It posts the derived keys to the board's `/v1/delivered` endpoint once, and
only after that write succeeds does it move the `board-synced` tag to the
pushed commit and push it, so the next run picks up exactly where this one
left off. A push whose commits deliver no keys records nothing and still
succeeds. A push with keys but no `TRACKER_BASE_URL` configured fails the
run, since a delivery that silently skips the board looks the same as one
that reached it.

## Gating a pull request on a board key

Every pull request also runs `cmd/pr-keys`, which fails the `pr-key-gate`
check when neither its commits nor its own title carries a board key. Both
halves are checked because either can be the one that reaches main: a merge
commit or a rebase delivers the range's commits, while a squash delivers
only the pull request's title, folding every commit into that one line. A
key has to lead a commit subject's key list (`MIT-123: fix the thing`) or
sit on its own `Jira:` trailer line, and the same leading-list rule applies
to the title — a key that only appears inside a merge commit's default
branch name is not delivery intent, and the gate does not count it.
