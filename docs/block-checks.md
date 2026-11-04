# Running the block checks on your block

This page is for a block author who wants the conformance verdict before
pushing. blockverify is the kit's command that runs every check the platform
holds a block to, so the verdict it prints on your machine is the one your
block's CI reports to the platform. The page covers what each check judges,
how you run it from your block's repository, and where CI runs it for you.

A block is a managed service defined by its `manifest.yaml`, and its operator
is the Go program that answers the platform's lifecycle calls, built in the
block's repository or, for an OpenTofu-only block, taken from
opentofu-operator. An attestation is the platform's signed record that a block
passed every check, which the catalog shows as a verified badge.

## What blockverify judges

blockverify runs seven suites over one block's directory, in this order. A
suite that fails turns the verdict to `fail`, and a partial pass is never
signed. A suite that does not apply to your block reports `skipped` with the
reason.

| Suite | What it checks |
|---|---|
| `trust-plumbing` | `manifest.yaml` exists and declares `contract: kit/v1`, your `go.mod` requires block-kit at or above the floor and no release of operator-kit, and a non-test file imports the kit's `handler`. No non-test file declares a function the kit owns, and a block that calls the GitHub API meets the rule in the next section |
| `contract` | A non-test file imports `handler`, which serves the wire contract. The kit's own tests check the handler against its goldens, the sample request and answer bodies in `contract/goldens` and `contract/response-goldens`, so the suite proves your block answers through that tested code and reports it as the contract verdict |
| `secret-safety` | A test whose name starts `TestNoSecretLeak` exists and passes. When a non-test file names the route `"/logs"`, a test named `TestNoSecretLeak_LogsEndpoint` must also exist and pass |
| `teardown` | A test whose name starts `TestUnsubscribeDeletes` exists and passes. It proves that an unsubscribe deletes what a subscribe made, by checking that your fake provider holds nothing for the subscription afterwards. For a block declaring a file-landing type it also runs `TestDeclaredResources`, whose `kittest.AssertBlock` reads the fake repository's main after unsubscribe, and names the path or branch it reports, as [Testing a block](testing.md#proving-a-block-that-lands-files) describes |
| `resources` | `TestDeclaredResources` exists, calls `kittest.AssertBlock` and passes, and the pass names the guards that ran |
| `deliverable` | The block has its own `Dockerfile`, and it copies nothing from `examples/operator-kit`, the folder of the platform's repository where the kit once lived, which a block's own build cannot reach |
| `manifest` | The manifest parses strictly as `mitstack/v1beta`, and `contract.CheckManifest`, the platform's own checks, accepts it against the block's `type-snapshot.json`, as [Writing the manifest](manifest.md) lists. On a pull request, a `spec.version` that differs from the base branch's manifest needs both `spec.releaseClass` and `spec.releaseNote`. A `spec.version` equal to the base branch's fails when the pull request changes any file other than Markdown, a file under `.github/` or `LICENSE`, and the message names the files and the missing version bump |

A file holding a `TestNoSecretLeak` or `TestUnsubscribeDeletes` test must not
build `kittest.FakeResource`, the kit's own stand-in block, because a test over
the kit's fake judges the fake instead of your block. [Testing a
block](testing.md) shows each of the three tests, and the guards
`kittest.AssertBlock` checks.

The functions the kit owns are the trust and event functions it implements
once for every block, such as `verifyEd25519Sig` and `EmitProgress`, which
`kitOwnedFuncs` in `verify/blockverify/trustplumbing.go` lists. A local copy
in your block would drift from the contract, so the suite refuses one by name.

The platform reads a release class as `correctness-fix`, `feature` or
`breaking`, and the release note says in one paragraph what the version
changes. In CI the kit's reusable workflow, described in [Where CI runs
it](#where-ci-runs-it), passes the base branch's manifest and the list of
files the pull request changes to blockverify. The list holds each changed
path ended by a NUL byte, as `git diff -z --name-only` writes it. On your
machine `--base-manifest <path>` and `--changed-files <path>` do the same,
as in this run against `origin/main`, where `<tag>` is a kit release as in
[Run it locally](#run-it-locally):

```sh
git show origin/main:manifest.yaml > /tmp/base-manifest.yaml
git diff --no-renames -z --name-only origin/main > /tmp/changed-files
go run github.com/mitstack-eu/block-kit/verify/blockverify@<tag> --standalone \
  --base-manifest /tmp/base-manifest.yaml --changed-files /tmp/changed-files .
```

A version bump is what publishes a change, because the workflow's publish
job never pushes over a version tag already in your image registry. So a
code or dependency change that keeps the base's version would merge green
and never ship, and the `manifest` suite fails it instead.

## How a block reports each resource's identity

Every resource a block creates reports its identity once it is observed
present. An identity is the set of values that address the resource at its
provider, such as `{"bucket": "app-state"}`. The platform records it and shows
it beside the resource, so a bill line or an audit finding traces to its app.

A block reports the identity through its ensure or its observe. Its ensure,
`EnsureResources` of `handler.ResourceReporter`, reports what it created. Its
observer reports what it read at the provider in the `Identity` field of the
`contract.Observation` it returns. An ensure that answers before its work
finishes has nothing to report yet, so its observer reports the identity once
the resource stands. Every shared observer in `observe` reports, on a present
answer, the identity it observed the resource by.

The `resources` suite checks the rule through `kittest.AssertBlock`, in
`verify/kittest/block.go`. After the subscribe, the check settles a pending
answer with `validate` calls, as the engine does. It then fails each resource
observed present with no identity, naming it, as in
`resource "aws.ebs-volume": observed present after subscribe with no identity`.

The check repeats the unsubscribe as the engine repeats a teardown. While the
answer is not `absent:true`, it sends the unsubscribe again, with each resource
an earlier answer observed absent recorded absent, so a block whose teardown
runs in the background passes once that teardown finishes. A resource still
standing after the check has re-sent the unsubscribe for 10 seconds fails,
named as in
`resource "aws.ebs-volume": observed present after unsubscribe re-sent for 10s, want absent`.

A block's test builds the block as production does, with the settings its
`main` uses. A setting only the test changes, such as a wait for the work to
finish before the subscribe answers, lets the check pass while production
records the resource with no identity.

## The GitHub API rule

The `trust-plumbing` suite holds a block that calls the GitHub API to one more
rule, so that a forged request cannot send the block's GitHub token to another
host. A request hands the operator each connection's secrets in `Secrets` and
its settings, such as its API endpoint, in `Instances`. The suite counts a
block as calling the API when a non-test file reads `Secrets["github"]` for
anything but its `owner`, reads `Instances["github"]`, or names
`api.github.com`. Such a block passes only when both of these hold:

- It resolves the API host a request supplies through the kit's `ghapi.Base`
  or `ghapi.BaseFromEnv`, which check it against an allowlist of hosts, or
  through a `urlguard.go` of its own whose `allowedGitHubHosts()` lists the
  hosts it trusts.
- It never builds a URL by appending to a variable named `apiBase`, the name
  the suite looks for as an unchecked host.

## The floor and operator-kit

The floor is the oldest kit release your `go.mod` may require, and today it is
`v0.4.0`, block-kit's first release. blockverify accepts any release at or
above it, `v0.5.0` and later included. The floor moves only by a deliberate
decision, so a new kit release never fails a block that has not picked it up
yet.

operator-kit, `github.com/mitstack-eu/operator-kit`, is the module the kit was
published under before block-kit. A block that still requires it, at any
version, fails, because block-kit is the one kit module the platform
supports. The wire contract is the same, so a block on operator-kit moves by
switching its `go.mod` requirement and its import paths once.

Every suite that reads the lifecycle contract requires `contract: kit/v1`,
the one contract this kit judges. A manifest in the retired `mitstack/v1alpha`
version fails every one of them, because the parser refuses it.

## Run it locally

The kit is a public module, so Go fetches it with no credential. Run the
suites from your block's directory, naming the kit release to run.
`--standalone` tells blockverify to judge that one directory on its own, and
it is the only mode the command has:

```sh
go run github.com/mitstack-eu/block-kit/verify/blockverify@v0.4.0 --standalone .
```

The version is required because `go run` with `@` builds the command outside
your block's module, so your own `go.mod` cannot choose it. A later tag works
the same way. The run takes about a minute, mostly spent compiling and
running your tests. A passing block ends with a line like this, where
`kit v0.4.0` is the floor the suites held your `go.mod` to:

```text
VERDICT: PASS — 7 suite(s), kit v0.4.0, standalone mode
```

A failing suite prints its reason on its own line, such as the missing test or
the manifest key the parser refused. Add `--json` for the machine-readable
report the attestation binds to.

## Where CI runs it

Your block's CI calls the kit's reusable workflow,
`.github/workflows/publish-block.yml`, with `uses:` and the block's id:

```yaml
jobs:
  block:
    uses: mitstack-eu/block-kit/.github/workflows/publish-block.yml@v0.4.0
    with:
      block-id: hetzner-firewall
      ecr-repository: mitstack-hetzner-firewall
      kit-ref: v0.4.0
```

`ecr-repository` names the repository in Amazon Elastic Container Registry
(ECR), AWS's image registry, that receives the image, and `kit-ref` the
block-kit tag the workflow builds blockverify and `blockprov` from. The
workflow also runs its scan scripts from that tag, so name the same tag in
`uses:` and in `kit-ref`. A mitstack
maintainer creates that ECR repository and a publish role in the platform
repository, mitstack-eu/mitstack, in `deploy/tofu/operators.tf` and
`deploy/tofu/block_publish.tf`. The maintainer then sets the
`AWS_BLOCK_PUBLISH_ROLE_ARN` variable on your repository to the role's ARN,
Amazon's identifier for it.

`ecr-repository` is optional, and the `publish` job runs only when you name
one. Leave it out when mitstack holds no ECR repository for your block, as for
a block repository the mitstack console or MCP server created in your own
GitHub organisation. The workflow then runs every check below, and the
attestation [`post-attestation`](#asking-the-platform-from-the-shared-workflow)
turns on, and publishes nothing. On a push to your default branch its
`no-registry` job runs in place of `publish` and writes this line to the run's
summary:

```text
No image registry was named in the ecr-repository input, so nothing was published.
```

Its `conformance` job runs on every event your CI calls it for. It runs your
tests and `blockverify --standalone`, and on a pull request it also runs
`commentcheck`, which judges the comments the change adds, as [Maintaining
the kit](maintainers.md#judging-the-comments-in-a-pull-request) explains. A
red verdict stops the `publish` job, which on a push to your default branch
builds the image, scans it and pushes it to the ECR repository you name, so
no image ships without a pass. The job then runs `blockprov`, which builds a
provenance record naming your repository, commit and CI run, signs it with
the run's GitHub Actions identity token and pushes it beside the image.

Two scan jobs also run on every event your CI calls the workflow for, and
`publish` waits for both. The `secrets` job runs `scripts/scan-secrets.sh`,
which scans your whole history, every commit your `HEAD` reaches, with the
secret scanner gitleaks 8.21.2, and names the commit that added a secret. The
`scripts` job runs `scripts/lint-scripts.sh`, which runs the shell-script
linter shellcheck 0.11.0 over every shell script your repository tracks. Both
scripts come from the kit at `kit-ref`. Each pins its scanner's sha256 in the
script and refuses to run a download whose sha256 differs.

To run either on your own checkout, clone the kit beside your block at the
tag your `kit-ref` names, shown as `<tag>`, and run it from your block's
directory:

```sh
git clone --branch <tag> https://github.com/mitstack-eu/block-kit ../block-kit
bash ../block-kit/scripts/scan-secrets.sh .
bash ../block-kit/scripts/lint-scripts.sh .
```

With the calling job named `block`, as above, the checks to require in your
repository's branch protection for `main` are `block / conformance`,
`block / secrets` and `block / scripts`.

When the platform creates a block's repository from its templates, which it
calls scaffolding, the repository also carries
`.github/workflows/block-verify.yml` and a token sealed to the block as the
repository secret `MITSTACK_API_TOKEN`. The token opens three routes, each for
that block alone: its manifest, its attestations and the dry-run apply. On
each push to `main` that workflow does three things with the token:

1. It fetches the platform's copy of the manifest from
   `GET https://api.mitstack.dev/v1/blocks/{id}/manifest` and writes it over
   `manifest.yaml`.
2. It runs `blockverify --standalone --json .` into `report.json`.
3. It posts the report to `POST https://api.mitstack.dev/v1/blocks/{id}/attestations`,
   and the platform signs a passing report as the block's attestation.

### How the publish job scans the image

The `publish` job builds your image and runs the kit's `scripts/scan-image.sh`
on it before any push. The script runs the image scanner Trivy 0.76.0,
checked by its sha256, from an empty directory with every `TRIVY_` variable
unset, so no ignore file or setting can silence a finding. Any finding that
has a fixed version, at any severity, stops the job, with the one exception
below. An error of Trivy's own stops the job too.

The exception covers [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932), an
advisory in the Go vulnerability database that marks the whole
`golang.org/x/crypto/openpgp` package unsafe. Its fix is a commit of
golang.org/x/crypto that no release names yet. Go knows that commit only by
the pseudo-version `v0.58.1-0.20261009181718-2f1f834330b2`, which Go's
version order puts above v0.58.0 and below v0.58.1. Every x/crypto release up
to v0.58.0 therefore counts as affected. Trivy reports the advisory in every
OpenTofu provider built on such a release, whether or not the provider uses
openpgp.

An OpenTofu provider is the plugin OpenTofu downloads to call a cloud's API,
and `tofu init` keeps it in the image's plugin cache. The exception covers a
provider binary in that cache and no other binary. Your block's own binary, such as its operator, keeps the full
refusal for GO-2026-5932, as for every other finding.
`scripts/go-2026-5932-exception.sh` accepts the finding only when each
condition in this table holds, and refuses it otherwise:

| Condition | How the script proves it |
|---|---|
| The binary is an OpenTofu provider the image installs | Its path in the image is `/var/cache/tofu-plugins/<host>/<namespace>/<type>/<version>/<platform>/terraform-provider-<type>`, with an optional `_` suffix. That directory is the plugin cache the opentofu-operator image names in `/etc/tofu.tfrc` |
| No openpgp code is compiled into it | `cmd/gofuncnames` prints the names in the binary's `.gopclntab` section. That is the table of every function, inlined ones included, that the Go runtime keeps for its stack traces. Stripping a binary removes its symbol table but leaves this one. The names must include at least one `golang.org/x/crypto` function and none under `golang.org/x/crypto/openpgp`. A refusal for openpgp code lists every openpgp name the table holds |
| The provider has shipped no release on a fixed x/crypto | The script reads the provider's newest release from `https://<host>/v1/providers/<namespace>/<type>/versions`. It then reads that release's `go.mod` from `https://raw.githubusercontent.com/<owner>/<repo>/v<release>/go.mod`. `<owner>/<repo>` comes from the binary's main module, the path `go version -m <binary>` prints on its `mod` line. The release's `golang.org/x/crypto` requirement must order below the fixed version above |

The script also runs govulncheck v1.9.0, the Go team's vulnerability scanner,
on each provider binary that holds the finding. Its install is checked against
its module sum, the hash the Go checksum database records for that release. It
prints govulncheck's result, which does not decide. A provider release is
stripped, which means it is built without the symbol table govulncheck reads
to see which packages a binary holds. govulncheck then judges by module
versions alone, which its output calls go.mod-level precision, and lists every
openpgp package as present. If govulncheck cannot run, the script prints why
and the function table alone decides.

The script names each binary by its path inside the image, with no leading
slash. For the AWS provider 6.71.0, one accepted finding reads like this:

```text
  govulncheck v1.9.0 against https://vuln.go.dev, modified 2026-11-02T22:01:43Z, ran at go.mod-level precision because the binary is stripped, so it cannot show which packages are present, and it does not decide:
    GO-2026-5932 at module golang.org/x/crypto@v0.58.0, and in 7 package(s): golang.org/x/crypto/openpgp, ...
accepted GO-2026-5932 in var/cache/tofu-plugins/registry.opentofu.org/hashicorp/aws/6.71.0/linux_amd64/terraform-provider-aws_v6.71.0: its Go function table names 366 golang.org/x/crypto functions and 0 in golang.org/x/crypto/openpgp, and registry.opentofu.org/hashicorp/aws's newest release 6.71.0 still requires golang.org/x/crypto v0.58.0, below the fix v0.58.1-0.20261009181718-2f1f834330b2
```

The exception ends for each provider on its own. Once a provider's newest
release requires the fixed x/crypto or none at all, the scan refuses that
provider's finding and names the release to lock, as in
`move the block to registry.opentofu.org/hashicorp/aws 6.72.0`. A registry or
`go.mod` read that gets no answer stops the job and names the read.

To scan an image you built yourself, install Docker, Go, jq and curl, and
clone the kit beside your block at the tag your `kit-ref` names. Then build
and scan the image from your block's directory:

```sh
git clone --branch <tag> https://github.com/mitstack-eu/block-kit ../block-kit
docker build -t my-block:dev .
bash ../block-kit/scripts/scan-image.sh my-block:dev
```

### Checks for an OpenTofu-only block

An OpenTofu-only block has no Go code of its own. It is an OpenTofu module, a
directory of `.tf` files that OpenTofu, the open-source infrastructure tool,
plans and applies. Its operator is opentofu-operator, a Go program built on
this kit that runs the module for each lifecycle call. The block's
`Dockerfile` builds on opentofu-operator's image, pinned by digest, and copies
the module to `/module`. Your CI sets `opentofu-only: true`:

```yaml
jobs:
  block:
    uses: mitstack-eu/block-kit/.github/workflows/publish-block.yml@<tag>
    with:
      block-id: opentofu-s3-bucket
      ecr-repository: mitstack-opentofu-s3-bucket
      kit-ref: <tag>
      opentofu-only: true
```

The workflow assumes two AWS roles, which two variables on your block's GitHub
repository name by their ARN, Amazon's identifier for a role. The platform
repository, mitstack-eu/mitstack, creates both roles in
`deploy/tofu/block_publish.tf` for each block that file lists in
`block_base_images`. A mitstack maintainer adds your block there and sets the
two variables.

| Variable | What its role may do | Which runs may assume it |
|---|---|---|
| `AWS_BLOCK_VERIFY_ROLE_ARN` | Pull the operator image, and nothing else | Your pull requests and pushes to `main` |
| `AWS_BLOCK_PUBLISH_ROLE_ARN` | Push your block's image, and pull the operator image | Pushes to `main` |

The `opentofu-conformance` job runs in place of the Go `conformance` job and
reports under the same check name, so `block / conformance` stays the check
your `main` requires. The operator image is private, so the job assumes the
verify role to pull it. The job builds your block's image once and runs three
checks inside it:

| Check | What it runs |
|---|---|
| `tofu fmt -check` | `tofu fmt -check -diff -recursive /module`, which fails on a file OpenTofu would reformat and prints the change |
| `tofu test` | `tofu init` with no backend, then `tofu test` over the module's `.tftest.hcl` files |
| `block-verify` | The test binary the operator image carries, which runs the kit's guards over the module. Its `TestDeclaredResources` fails, for example, when `manifest.yaml` declares a resource that `resources.json` does not map to the module |

block-verify judges an OpenTofu-only block as blockverify judges a Go block's
code. It comes from the operator image, while the Go job builds blockverify
from the kit at `kit-ref`.

On a push to your default branch, the `publish` job builds, scans and pushes
the image as it does for a Go block. The job runs block-verify again in the
image it pushed and hands that output to `blockprov build --block-verify`.
The provenance record's `conformance` then carries block-verify's verdict and
the sha256 of its output. Its `suiteVersion`, the field naming the checks that
judged the block, reads `block-verify@<kit version>`. The record's kit version
is the block-kit version the operator binary in the image was built with,
because the block has no `go.mod` to read it from.

Because block-verify and the operator binary both come from your block's
image, the record trusts what that image carries. A `Dockerfile` that builds
on another image, or replaces `/usr/local/bin/block-verify`, changes the
verdict and the kit version the record carries. So review a change to the
`Dockerfile` as you would a change to the checks themselves.

### Asking the platform from the shared workflow

The reusable workflow runs the same calls itself when your caller sets two
inputs and passes the secret:

```yaml
jobs:
  block:
    uses: mitstack-eu/block-kit/.github/workflows/publish-block.yml@<tag>
    with:
      block-id: "@acme/billing-db"
      ecr-repository: mitstack-billing-db
      kit-ref: <tag>
      platform-verdict: true
      post-attestation: true
    secrets: inherit
```

With `platform-verdict: true`, the `platform-verdict` job runs on a pull
request. It posts `manifest.yaml` to
`POST https://api.mitstack.dev/v1/blocks/apply?dryRun=true`. The platform runs
every check a real apply runs, the ones that read its own records included,
and writes nothing. A refusal fails the job with the platform's status and
reason, such as `HTTP 400: {"error":"spec.params is not a field of a
mitstack/v1beta manifest"}`. Add `block / platform-verdict` to the checks
your `main` requires, so a refused manifest cannot merge.

GitHub hands no secret to a pull request from a fork, so on such a pull
request the job fails with `platform-verdict needs the MITSTACK_API_TOKEN
secret`. A maintainer then pushes the branch to the block's own repository to
get the verdict.

With `post-attestation: true`, the `attest` job runs on a push to your default
branch once `conformance`, `secrets` and `scripts` pass. It fetches the
platform's manifest, runs blockverify on it, and posts the report to the
attestations route, as the scaffolded workflow does. A block on the shared
workflow then drops its own `block-verify.yml`. The `attest` job needs the Go
`conformance` job, so it does not run for an OpenTofu-only block.

Both inputs default to `false`, and `api-url` names the platform, which
defaults to `https://api.mitstack.dev`. A block whose repository you created
yourself mints its token on the API Tokens page of the mitstack console and
stores it in the same secret.
