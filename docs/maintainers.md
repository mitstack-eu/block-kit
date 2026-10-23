# Maintaining the kit

This page is for the people who change block-kit itself: how the kit is
tested, how a release is cut, and the checks every pull request and every push
to `main` runs. A block author needs none of it, except the comment check,
which a block's own CI runs too.

## Testing the kit

The kit's module is stdlib-only, and no third-party dependency is ever added.
From the repository's root:

```sh
go vet ./... && go test ./...
go -C examples/hetzner-firewall test ./...
go run ./verify/blockverify --standalone examples/hetzner-firewall
```

The example block is a Go module of its own, so `./...` leaves it out and CI
tests it by itself. The example's `go.mod`
builds it against the kit in the same checkout. A test in `examples/` holds
every README code fence whose lead-in links a file under `examples/` equal to
that file, so a change to the example's `manifest.yaml` or `block.go` changes
the README in the same commit.

`contract/manifest.schema.json` is generated from the manifest's Go types, and
a contract test fails when the committed file differs from what they generate.
Regenerate it with `go run ./cmd/manifest-schema > contract/manifest.schema.json`.

## Release

block-kit's first release, v0.4.0, is tagged by hand, because the release
script cuts no tag in a repository that holds no kit tag yet. The version a
module starts at is a choice, not arithmetic.

After the first tag, every push to `main` that changes the tree cuts the next
patch tag, through `scripts/release-block-kit.sh`, which CI runs. A commit message carrying a `kit-release: minor` line raises
the minor level instead and zeroes the patch level. A skipped or failed
release run is not lost: the next push resolves its base from the newest tag
and cuts the release the earlier push owed.

A change that breaks the Go API, such as a package move, takes
`kit-release: minor`, which opens the next v0 line, such as v0.5.0. No commit
carries `kit-release: major` before the commercial launch, because that raise
cuts v1.0.0, a version kept for the launch.

## Gating and recording a pull request's board keys

The `pr key gate` and `record delivery` jobs run `pr-keys` and
`board-delivery` from mitstack-eu/plain-delivery at its release tag `v0.1.0`,
and that repository's README says what each command does.

## Judging the comments in a pull request

`cmd/commentcheck` judges the Go and TypeScript comments a pull request adds,
comparing the working tree with the pull request's base commit. The kit's
`comment check` job runs it on every pull request, and the conformance job of
`publish-block.yml` runs it on a block's pull request.

The check fails when an added comment holds an issue key, which is `MIT-` or
`MS-` followed by digits, or a `file:line` reference. A comment says what the
code does or why, and the key belongs in the commit subject. It also fails
when a file the pull request adds, changes or renames holds a comment share
above its target, whatever the file held at the base. A file's comment share
is the fraction of its lines that are comment lines, and the target is 7% for
a production file and 6% for a test file, such as a `_test.go` file. A file
the pull request leaves alone is not judged, nor is a deleted or generated
file.

In the kit's repository, run the check against the commit your branch started
from:

```sh
base="$(git merge-base origin/main HEAD)"
go run ./cmd/commentcheck -base "$base" .
go run ./cmd/commentcheck -base "$base" -same-code .
go run ./cmd/commentcheck -report cmd/commentcheck
```

The second command proves a change only removes or rewrites comments, and it
fails naming any file whose code differs from the base. The third prints the
comment share of a file or directory and needs no base. In a block's
repository, run the released command instead:

```sh
go run github.com/mitstack-eu/block-kit/cmd/commentcheck@v0.4.0 -base "$(git merge-base origin/main HEAD)" .
```

The check reads the working tree as the head, so a file you moved without
`git mv` is judged as a new file until you stage the move.
