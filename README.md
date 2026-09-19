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
