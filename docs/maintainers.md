# Maintaining the kit

This page is for the people who change block-kit itself: how the kit is
tested, how a release is cut, and the checks every pull request and every push
to `main` runs. A block author needs none of it, except the comment check,
`commentcheck`, and the wait check, `checkpolls`, which a block's own CI runs
too.

## Testing the kit

The kit's module is stdlib-only, with one exception. Its `go.mod` requires
`golang.org/x/crypto`, whose ChaCha20-Poly1305 `helpers/age` seals a snapshot
with, because hand-written cryptography is a security risk and this library is
the Go team's own. That module builds on `golang.org/x/sys`, which `go.mod`
lists as an indirect requirement. No other third-party dependency is ever
added. The Robot
rescue system's SSH session sits in a Go module of its own,
`capabilities/hetzner/robot/rescuessh`, with its own `golang.org/x/crypto`
requirement. From the repository's root:

```sh
go vet ./... && go test ./...
go -C capabilities/hetzner/robot/rescuessh test ./...
go -C helpers/healthz test ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -C capabilities/hetzner/robot/rescuessh -scan package ./...
go -C examples/hetzner-firewall test ./...
go run ./verify/blockverify --standalone examples/hetzner-firewall
```

The tools image builds with Docker from the repository's root, since it also
holds the sign-in companion built from `cmd/mitstack-signin`, which [the helpers page](helpers.md#opening-an-app-only-to-its-orgs-members) describes, and the health probe built from `cmd/mitstack-probe`. A test behind
the `toolsimage` build tag checks each file it holds against
`helpers/tools/tools.sha256` and runs the companion and the probe. CI runs them on every
event:

```sh
docker build --platform linux/amd64 -f helpers/tools/Dockerfile -t block-kit-tools:ci .
TOOLS_IMAGE=block-kit-tools:ci go test -tags toolsimage -run '^TestToolsImage_(HoldsEveryFileAtItsPinInALayerOfItsOwn|RunsTheSignInCompanion|RunsTheProbe)$' ./helpers/tools
```

A test behind the `edgecaddy` build tag runs the sign-in route `Edge.Site`
renders in a real caddy, on the host's network, and proves that a header a
visitor sends under a member header's name never reaches the app. CI runs it
against `caddy:2`, the image every Docker host's edge pulls:

```sh
CADDY_IMAGE=caddy:2 go test -tags edgecaddy -run '^TestEdge_' ./helpers/dockerhost
```

The rescuessh module, the healthz module and the example block are Go modules
of their own, so `./...` leaves them out and CI tests each by itself. The
healthz module is one so that an operator on any kit release can mount the
health handler without moving its kit pin. CI also runs govulncheck over
every package the rescuessh module imports, and a known vulnerability in any
of them fails the run. That scan does not cover the kit's own module. The
example's `go.mod`
builds it against the kit in the same checkout. A test in `examples/` holds
every README code fence whose lead-in links a file under `examples/` equal to
that file, so a change to the example's `manifest.yaml` or `block.go` changes
the README in the same commit.

`contract/manifest.schema.json` is generated from the manifest's Go types, and
a contract test fails when the committed file differs from what they generate.
Regenerate it with `go run ./cmd/manifest-schema > contract/manifest.schema.json`.

The publish job's image scan accepts the advisory GO-2026-5932 in an OpenTofu
provider binary that holds no openpgp code, as
[How the publish job scans the image](block-checks.md#how-the-publish-job-scans-the-image)
describes. The tests of that exception, in
`scripts/go_2026_5932_exception_test.go`, build stand-in provider binaries
from `scripts/testdata/fixture-provider`. That fixture is a module of its own
requiring golang.org/x/crypto v0.58.0, a release the advisory covers, so the
kit's own `go.mod` never carries that release. The `openpgp` build tag adds an openpgp call, and the `nocrypto` tag
drops x/crypto altogether.

The tests of the dated exception, in the same file, put a stand-in file at the
release binary's path. A stand-in `date` command on `PATH` sets the day the
script reads as today. A stand-in `sha256sum` answers with the release's
digest where a case needs it, since no fixture can hold the release's bytes.
The proof workflow below scans the real binary.

`scripts/testdata/vulndb` stands in for the Go vulnerability database at
vuln.go.dev and holds GO-2026-5932 alone, so the tests never read vuln.go.dev.
A local server answers the registry and `go.mod` reads. The tests still fetch
golang.org/x/crypto and govulncheck through the Go module proxy, so they need
network access. `scripts/scan_image_test.go` drives `scripts/scan-image.sh`
with stand-in `trivy` and `docker` commands, so it needs neither installed.

A pull request that changes the scan also runs
`.github/workflows/scan-image-proof.yml`. It builds one image for each row of
its `matrix`: the AWS provider 6.73.0 and the Scaleway providers 2.89.0 and
2.92.1, each fetched by the checksum the registry publishes. It runs
`scripts/scan-image.sh` on each image. A row lists the scan's exit, the
findings the scan must accept in that binary and refuse nowhere, and the
findings it must refuse there and accept nowhere. GO-2026-5932 is listed as
accepted in each row.

The AWS scan is expected to pass. When a new advisory that has a fix covers
that AWS release, its job fails and prints a `refused` line naming the
advisory and the versions that fix it. The repair is to move the AWS row of
the `matrix` in `scan-image-proof.yml` to a provider release built on a fixed
version, with the `shasum` the registry publishes for it, and to leave the scan
as it is. The Scaleway 2.89.0 release
also carries x/net and
standard-library findings that have fixes, which the scan refuses as it should,
so its scan is expected to exit 1. Its row lists the dated exception's three
findings as refused, which proves the exception covers no other binary. The
2.92.1 row lists them as accepted, so its scan exits 0. Once the dated
exception's last day passes, the 2.92.1 job
fails until its row changes. To repeat the proof
on `main` with no change, start the workflow by hand from the repository's
Actions tab, which its `workflow_dispatch` trigger allows.

## The Go release the kit builds on

`.github/workflows/publish-block.yml` names the Go release once, as the default
of its `go-version` input. A block's CI installs that release when its `ci.yml`
passes no `go-version` of its own. The kit's other workflows name the same
release in each step that installs Go with the `actions/setup-go` action. So
do the two Dockerfiles that compile a Go program on a `golang` image,
`helpers/tools/Dockerfile` and `examples/hetzner-firewall/Dockerfile`.

`scripts/go_release_agreement_test.go` reads the default and fails each place
that names another release:

```sh
go test ./scripts/ -run TestGoRelease_
```

Each failure names one place by file and line, as in this one:

```text
.github/workflows/ci.yml:22 names Go 1.26.10; publish-block.yml installs Go "1.26.11" by default
```

To move the kit to a new release, such as 1.26.12, change the default, run the
suite, and change each line it prints. For both Dockerfiles, run
`docker buildx imagetools inspect golang:1.26.12-alpine` and copy the value of
its `Digest:` line after the tag, as `golang:1.26.12-alpine@sha256:...`. Docker
builds from the digest and ignores a tag that disagrees with it, so a stale
digest keeps the old Go under a new tag. The suite reads the tag alone and
cannot see a stale digest. The `go` line of the kit's `go.mod`
stays where it is, since it is the oldest Go a block importing the kit may
build on.

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

Each kit tag also publishes the tools image,
`ghcr.io/mitstack-eu/block-kit-tools`, which `helpers/tools/Dockerfile` builds
and blocks copy their tools from. The `release` job in
`.github/workflows/ci.yml` takes these steps:

1. It runs the release script's dry run, which hands the job the planned tag.
2. It copies cosign, the image signing tool, out of its publisher's image with
   `scripts/copy-tool.sh`, a link to `helpers/tools/scripts/copy-tool.sh`,
   which sits in the Go package because `go:embed` reads only files inside it.
3. It pushes the tools image under the planned tag and signs its digest keyless
   with the run's identity.
4. It verifies the signature and pulls the image with no login, which fails
   while the package is private. GitHub makes a new package private, so an
   owner makes it public once in the package's settings, and the job then
   reruns.
5. It runs the toolsimage tests in `helpers/tools`, which copy every file out
   of the published image through Docker and over the registry's HTTP API.
6. It runs the release script with `--tools-image`, which cuts the tag on a
   commit on top of head. That commit writes the image's reference into
   `tools.Image` in `helpers/tools/tools.go`, since a digest cannot sit in the
   commit its image is built from. `main` itself names no image, and its
   `tools.Image` is empty.

A tag therefore points at a commit `main` never holds. The next push still
finds its base from that tag, since the tag's commit has `main`'s commit as
its parent. A block depends on a kit tag, never on a pseudo-version of a `main`
commit, since Go derives that version from an older tag and its `tools.Image`
is empty.

The rescuessh module is released by its own tags, such as
`capabilities/hetzner/robot/rescuessh/v0.1.0`, the form Go resolves for a
module inside a repository. Its first tag is cut by hand, as the kit's was.
After it, the
release job runs `scripts/release-block-kit.sh --module
capabilities/hetzner/robot/rescuessh`, which judges changes under that path
alone and follows the same rules as the kit's tags.

The healthz module is released the same way, by tags such as
`helpers/healthz/v0.1.0`. Its first tag is cut by hand too. After it, the
release job runs `scripts/release-block-kit.sh --module helpers/healthz`.

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

The `block / delivery` and `record delivery` jobs run `pr-delivery` and
`board-delivery` from mitstack-eu/plain-delivery at the release tags that
`ci.yml` pins, and that repository's README says what each command does.
`pr-delivery` fails a pull request whose code lands before its tests, or
whose keys are not issues claimed on the board. `publish-block.yml` runs the
same release of it for a block, so a change to that pin edits both workflows.

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

## Judging the waits a pull request adds

A wait in a block or in the kit lasts until its upstream, the system it waits
on, is done. GitHub finishing a CI build is one upstream, and a cluster node
coming up is another. A fixed sleep or a timer either waits longer than it
needs or gives up before the upstream is done, so a wait proceeds on the
signal its upstream sends, such as a webhook GitHub calls or a report a node
sends to the platform.

Where the upstream sends no signal, the wait still adds no timer of its own:

- An operator verb answers `pending:true`, and the platform's engine asks
  again with `validate`, leaving a longer gap before each new ask, as [Work
  that outlasts one call](handler.md#work-that-outlasts-one-call) explains.
- Other code calls the waiter its upstream's own client provides, a function
  that returns once the upstream reports the work done, such as
  `ActionClient.WaitFor` in hcloud-go, Hetzner Cloud's Go client.
- Where neither exists, the waiter is written once for that upstream, in the
  kit or in the engine, before the change that needs it.

`cmd/checkpolls` holds a pull request to that. It compares the working tree
with the pull request's base commit, untracked files included, and fails on
a line the change adds in one of these forms:

| Language | Files it judges | The form that fails |
|---|---|---|
| Go | `.go` and `.go.tmpl` | A `time.Sleep(`, `time.Tick(` or `time.NewTicker(` call |
| TypeScript and JavaScript | `.ts`, `.tsx`, `.js`, `.jsx`, `.mjs` and `.cjs` | A `setInterval(` call or a `refetchInterval` option |
| Shell | Every file but prose, such as Markdown and plain text | `sleep` with a number or a variable, run as a command: at the start of a line, after an opening quote, after a workflow's `run:` key, or after a keyword or operator such as `do` or `&&` |

Shell runs from many file types. A `.sh` script holds it, and so does the
`run:` key of a GitHub Actions workflow, which holds a job's shell commands,
and so does a workflow template held in a Go string. So the shell form judges
every file that is not prose. A line that opens with a comment marker, `//` or
a shell `#`, runs nothing and is not judged. A quoted string that opens with
`sleep` and a number is judged as a command, since a `.tf` file or a Go call
can hand that string to the shell, so word a message string another way.

Each failure names the file and line, and the run ends with the advice:

```text
.github/workflows/ci.yml:8: adds a shell sleep: - run: sleep 10
checkpolls: found 1 added line holding a fixed sleep or a polling interval. A wait proceeds on the signal its upstream sends. Where it sends none, an operator verb answers pending and the engine asks again later, and other code calls the waiter its upstream's client provides, as block-kit's docs/maintainers.md explains.
```

The check judges only the lines a change adds, so it takes no flag but
`-base` and reads no list of paths or lines to skip. A poll the base already
holds passes until a change rewrites its line.

It cannot see a timer reset inside a loop, a `time.After` in a `select`, or a
cron schedule. Nor can it see a new loop on `runEvery`, the platform engine's
function that runs a job on a fixed interval. A reviewer judges those against
the same standard.

The kit's `test` job in `ci.yml` runs the check on every pull request to the
kit. In a block's CI, `publish-block.yml` runs it on every pull request in
both of its conformance jobs: `conformance` for a Go block and
`opentofu-conformance` for an OpenTofu block. Each builds the check from the
kit at `kit-ref`, the kit tag a block's CI names, as [Where CI runs
it](block-checks.md#where-ci-runs-it) explains. The block's own CI workflow
file, the one that calls `publish-block.yml`, names the kit twice: the tag on
its `uses:` line picks the version of `publish-block.yml` that runs, and
`kit-ref` picks the version of the tools it builds. So a block gets the check
once that file moves both to a kit release whose tree holds `cmd/checkpolls`.

In the kit's repository, run it against the commit your branch started from:

```sh
go run ./cmd/checkpolls -base "$(git merge-base origin/main HEAD)"
```

In a block's repository, run the released command at the tag your `kit-ref`
names, shown as `<tag>`, so your run matches your CI:

```sh
go run github.com/mitstack-eu/block-kit/cmd/checkpolls@<tag> -base "$(git merge-base origin/main HEAD)"
```
