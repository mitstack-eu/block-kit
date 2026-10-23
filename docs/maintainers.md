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
go -C board vet ./... && go -C board test ./...
go -C examples/hetzner-firewall test ./...
go run ./verify/blockverify --standalone examples/hetzner-firewall
```

The board module and the example block are Go modules of their own, so
`./...` leaves both out and CI tests each by itself. The example's `go.mod`
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

After the first tag, every push to `main` that changes the tree outside
`board/` cuts the next patch tag, through `scripts/release-block-kit.sh`,
which CI runs. A commit message carrying a `kit-release: minor` line raises
the minor level instead and zeroes the patch level. A skipped or failed
release run is not lost: the next push resolves its base from the newest tag
and cuts the release the earlier push owed.

A change that breaks the Go API, such as a package move, takes
`kit-release: minor`, which opens the next v0 line, such as v0.5.0. No commit
carries `kit-release: major` before the commercial launch, because that raise
cuts v1.0.0, which the launch keeps.

## Recording a delivery on the board

The board is Plain, mitstack's work tracker, whose items are issues with keys
such as `MIT-123`. Every push to `main` records which issues it delivered,
through `board/cmd/board-delivery`. That command, `board/cmd/pr-keys` and the
`board/boardkeys` package they share form the board module,
`github.com/mitstack-eu/block-kit/board`. It has its own `go.mod`, so the
kit's module leaves it out, and a change to it cuts no kit tag. Its `go.mod`
pins the kit at a commit of `main`, so bump that pin by hand when the board
module needs newer kit code. The pinned commit still holds `safe` at the root,
so a bump past it also changes `board-delivery`'s import to `helpers/safe`.

The board module has no tag of its own, so another repository installs it at
a commit of this repository's `main`, which Go names by a pseudo-version. A
pseudo-version names one commit forever, so a consumer moves to newer board
code only by editing its own pin. This install pins the commit `6dbf4157af66`:

```sh
go install github.com/mitstack-eu/block-kit/board/cmd/board-delivery@v0.0.0-20261023064823-6dbf4157af66
```

`boardkeys` derives the keys a commit delivers. A key counts only when it sits
in a leading key list in the commit's subject, closed by a colon, or on a
whole-line `Jira:` trailer in the body. A key anywhere else in a subject or
body is prose and delivers nothing. So is a reference, a key in a
`(refs MIT-123)` group closing the subject or on a `Jira-refs:` trailer line,
which names an issue without delivering it.

`board-delivery` reads a local `board-synced` git tag as the anchor for its
range: the commits since that tag, or since the push event's `before` SHA when
no tag exists yet, or the single pushed commit when neither is usable. It
posts the derived keys to the board's `/v1/delivered` endpoint once. Only after
that write succeeds does it move the `board-synced` tag to the pushed commit
and push it, so the next run picks up where this one left off. A push whose
commits deliver no keys records nothing and still succeeds. A push with keys
but no `TRACKER_BASE_URL` fails the run, since a delivery that silently skips
the board looks the same as one that reached it.

## Gating a pull request on a board key

Every pull request also runs `board/cmd/pr-keys`, which fails the
`pr key gate` check when neither its commits nor its title carry a board key.
Both halves are checked because either can reach `main`: a merge commit or a
rebase delivers the range's commits, while a squash delivers only the pull
request's title.

A key counts in two forms. A delivering key leads the subject or title in a
key list closed by a colon, as in `MIT-123: fix the thing`, or sits on its own
`Jira:` trailer line, and it moves the issue to Done when the change reaches
`main`. A reference closes the subject or title in a group, as in
`fix the thing (refs MIT-123)`, or sits on a `Jira-refs:` trailer line, and it
leaves the issue's status alone. An issue whose change also needs a platform
or block release takes the reference here, so it closes only when that later
delivery lands.

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
