# Maintaining the kit

This page is for the people who change block-kit itself: how the kit is
tested, how a release is cut, and the checks every pull request and every push
to `main` runs. A block author needs none of it, except the comment check,
which a block's own CI runs too.

## Testing the kit

The kit's module is stdlib-only, and no third-party dependency is ever added.
The Robot rescue system's SSH session needs `golang.org/x/crypto/ssh`, so it
sits in a Go module of its own, `capabilities/hetzner/robot/rescuessh`, and the
kit's `go.mod` still requires nothing. From the repository's root:

```sh
go vet ./... && go test ./...
go -C capabilities/hetzner/robot/rescuessh test ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -C capabilities/hetzner/robot/rescuessh -scan package ./...
go -C examples/hetzner-firewall test ./...
go run ./verify/blockverify --standalone examples/hetzner-firewall
```

The rescuessh module and the example block are Go modules of their own, so
`./...` leaves them out and CI tests each by itself. The rescuessh module is
the only code here with a third-party dependency, so CI also runs govulncheck
over every package it imports, and a known vulnerability in any of them fails
the run. The example's `go.mod`
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
patch tag, through `scripts/release-block-kit.sh`, which CI runs. A commit
message carrying a `kit-release: minor` line raises the minor level instead
and zeroes the patch level, so v0.4.3 is followed by v0.5.0. A skipped or
failed release run is not lost: the next push resolves its base from the
newest tag and cuts the release the earlier push owed.

A change that breaks the Go API, such as a package move, takes
`kit-release: minor`, which opens the next v0 line, such as v0.5.0. A
`kit-release: major` line would raise a v0 tag to v1.0.0, which is cut by hand
at the commercial launch. So while the newest tag is a v0 version, the script
refuses that line, cuts no tag and fails. Go never reuses a published version,
so a v1.0.0 cut by mistake would stay on the Go module proxy as the kit's
newest release.

The rescuessh module is released by its own tags, such as
`capabilities/hetzner/robot/rescuessh/v0.1.0`, the form Go resolves for a
module inside a repository. Its first tag is cut by hand, as the kit's was.
After it, the
release job runs `scripts/release-block-kit.sh --module
capabilities/hetzner/robot/rescuessh`, which judges changes under that path
alone and follows the same rules as the kit's tags.

The release job runs only after a merge, so a pull request carrying the line
has to fail before it merges. The required `test` job in
`.github/workflows/ci.yml` runs `scripts/release-block-kit_test.sh` on every
run. On a pull request it also runs the script's dry run over the pull
request's own commits. To check a branch before you open its pull request, run
the same dry run:

```sh
bash scripts/release-block-kit.sh --dry-run --base origin/main --head HEAD
```

## Gating and recording a pull request's board keys

The `pr key gate` and `record delivery` jobs run `pr-keys` and
`board-delivery` from mitstack-eu/plain-delivery at the release tag that
`ci.yml` pins, and that repository's README says what each command does.

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
