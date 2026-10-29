# Running the block checks on your block

This page is for a block author who wants the conformance verdict before
pushing. blockverify is the kit's command that runs every check the platform
holds a block to, so the verdict it prints on your machine is the one your
block's CI reports to the platform. The page covers what each check judges,
how you run it from your block's repository, and where CI runs it for you.

A block is a managed service defined by its `manifest.yaml`, and its operator
is the Go program in the block's repository that answers the platform's
lifecycle calls. An attestation is the platform's signed record that a block
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
| `teardown` | A test whose name starts `TestUnsubscribeDeletes` exists and passes. It proves that an unsubscribe deletes what a subscribe made, by checking that your fake provider holds nothing for the subscription afterwards |
| `resources` | `TestDeclaredResources` exists, calls `kittest.AssertBlock` and passes, and the pass names the guards that ran |
| `deliverable` | The block has its own `Dockerfile`, and it copies nothing from `examples/operator-kit`, the folder of the platform's repository where the kit once lived, which a block's own build cannot reach |
| `manifest` | The manifest parses strictly as `mitstack/v1beta` and its `metadata.id` is a block id. On a pull request, a `spec.version` that differs from the base branch's manifest needs both `spec.releaseClass` and `spec.releaseNote`. A `spec.version` equal to the base branch's fails when the pull request changes any file other than Markdown, a file under `.github/` or `LICENSE`, and the message names the files and the missing version bump |

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
block-kit tag the workflow builds blockverify and `blockprov` from.

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
`.github/workflows/block-verify.yml` and an org API token as the repository
secret `MITSTACK_API_TOKEN`. On each push to `main` that workflow does three
things with the token:

1. It fetches the platform's copy of the manifest from
   `GET https://api.mitstack.dev/v1/blocks/{id}/manifest` and writes it over
   `manifest.yaml`.
2. It runs `blockverify --standalone --json .` into `report.json`.
3. It posts the report to `POST https://api.mitstack.dev/v1/blocks/{id}/attestations`,
   and the platform signs a passing report as the block's attestation.

A block whose repository you created yourself runs the same three commands in
a workflow of its own, with an org API token in the same secret. You mint that
token on the API Tokens page of the mitstack console.
