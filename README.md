# operator-kit

This module is the contract every mitstack block imports to talk to the
engine. It covers the webhook trust boundary a block's handler sits behind,
the events a block emits as it works, and `kittest`, the conformance harness
a block's own tests run against the same fixtures the engine is tested
against. The `contract` package owns the wire contract itself: the envelope
and response JSON schemas, and the golden request and response fixtures both
sides are checked against.

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
are handed an `Identity` and nothing else. `Request.Desired()` returns a
`contract.Desired`, which embeds `Identity` and adds the params, secrets
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
