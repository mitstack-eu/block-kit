// Command blockverify is the standalone conformance-suite runner for a
// mitstack block that lives OUTSIDE this monorepo (MIT-1626 AC#3,
// MIT-1586/1588). An external author's own CI invokes it as:
//
//	go run github.com/mitstack-eu/operator-kit/blockverify@<tag> \
//	  --json --standalone [--base-manifest <path>] .
//
// --base-manifest names the base branch's manifest.yaml, and may appear
// anywhere on the command line; its path is never taken for the target. The
// flag with no path after it is a usage error.
//
// # One truth, two doors
//
// scripts/block-verify.sh remains THE tool for verifying blocks that live
// inside this repository (repo mode: scans this repo's own examples/ tree,
// no <target> or a <target> under this repo, --standalone not passed). This
// command is its external-repo twin — same suites, same report shape, same
// verdict semantics — for the case scripts/block-verify.sh cannot reach at
// all: an author's block has no checkout of this monorepo and no access to
// its scripts/. That is the same one-truth caveat package ghapi documents
// for its own host-allowlist decision — the CONTRACT (report shape, suite
// set, run order, digest algorithms) is single-sourced in
// scripts/block-verify.sh's header comment, and this command is a
// from-scratch REIMPLEMENTATION of its standalone-mode behaviour in Go, not
// a wrapper around the shell script (which this module cannot invoke — it
// has no access to a checkout of this repo either). Anything that changes
// the standalone contract must change both. The resources, manifest and
// naming suites are the three exceptions: they are blockverify's own and
// have no counterpart in the script.
//
// blockverify supports ONLY standalone mode. It refuses (exit 2, naming
// scripts/block-verify.sh) if --standalone is not supplied, or if
// --standalone is supplied with no target directory — there is no
// "guess repo mode" fallback here, because there is no repo to guess.
//
// # Which rule judges a target
//
// The target's manifest.yaml declares the lifecycle contract it serves at
// spec.lifecycle.contract, and trust-plumbing and contract judge the target
// by that contract, the same branch the platform's operator gates take. The
// value is read through contract.ParseManifest under either manifest
// version, so a contract: key inside a block scalar or nested under another
// field is not the contract, and a lifecycle mapping with no contract key
// declares none.
//
//   - No declared contract, or no manifest at all: the legacy rule below.
//   - kit/v1: kitshim.go and the three TestContract_* functions are waived,
//     because the kit owns the trust boundary, the /hook route and the
//     webhook-contract goldens. In their place, some file under the target
//     must import github.com/mitstack-eu/operator-kit/handler, as package
//     kitbinding defines it: an import declaration in a non-test .go file a
//     default build compiles, outside testdata/, vendor/ and directories
//     starting with "_" or ".". A comment or string naming the path does not
//     count, and an import in a _test.go file binds a test, not what the
//     block serves. A file the scan cannot read, parse or match against its
//     build constraints fails both suites as "could not scan", naming it.
//     Both suites report pass or fail with a detail naming the kit/v1 rule.
//   - Any other value: both suites fail, naming the value.
//   - A manifest that is present but cannot be opened (a directory, a
//     dangling symlink) or that contract.ParseManifest refuses: both suites
//     fail as a tooling failure naming the target's manifest.yaml and the
//     reason, rather than judging it by the legacy rule.
//
// # What it checks, in run order
//
//  1. trust-plumbing — reimplements scripts/check-operator-kit-pin.sh: the
//     target's go.mod requires the operator-kit module at or above the
//     kit-pin floor version, kitshim.go exists (on kit/v1, the handler
//     import instead), and no other non-test, non-shim .go file redeclares
//     a kit-owned symbol (a purely TEXTUAL/
//     static check, so it still produces a meaningful failure on a target
//     whose module does not compile — e.g. because it redeclares a
//     kit-owned symbol, which is itself a build error). This suite also
//     requires manifest.yaml to exist (a standalone target has no other
//     suite positioned to catch that).
//  2. contract — reimplements scripts/check-operator-contract-tests.sh: an
//     explicit PRESENCE check for TestContract_DecodesEveryValidGolden,
//     TestContract_RejectsMalformedGoldens and
//     TestContract_EmitsConformingResponse BEFORE running
//     `go test -run TestContract_ ./...` in the target — presence is
//     checked first because `go test -run` with zero matching tests exits 0
//     (a vacuous pass), which is exactly the gap that presence check
//     exists to close. On kit/v1 no go test runs, and the handler-import
//     check takes the presence check's place, so the suite never passes
//     vacuously.
//  3. secret-safety — reimplements scripts/check-operator-secret-leak-tests.sh:
//     presence of TestNoSecretLeak (+ the TestNoSecretLeak_LogsEndpoint
//     variant when the target serves the /logs ring buffer), and — unlike
//     that script, which is presence-only — actually RUNS the test(s).
//  4. teardown — reimplements scripts/check-operator-teardown-tests.sh:
//     presence of TestUnsubscribeDeletes* AND a real run of it (MIT-1124's
//     teardown contract; a broken teardown test must fail this gate, not
//     merely be absent).
//  5. resources — for a target whose manifest.yaml is mitstack/v1beta,
//     presence of a TestDeclaredResources test that calls
//     kittest.AssertDeclaredResources, AND a real run of it (see "The
//     resources suite" below). Skipped when the manifest is not
//     mitstack/v1beta, since only that version declares resources, or when
//     there is no manifest.yaml.
//  6. deliverable — the target's OWN Dockerfile must exist and must not
//     COPY a monorepo-relative build context (examples/operator-kit) it
//     does not have as a standalone repository.
//  7. manifest — checks the names the target's manifest.yaml declares
//     against the resource registry package contract owns, its metadata.id
//     against the block id grammar (see "The manifest suite" below).
//     Skipped when there is no manifest.yaml, which trust-plumbing already
//     fails.
//  8. naming — checks the app-name limit the target's manifest.yaml
//     declares at spec.naming.appName against the derived names behind it
//     (see "The naming suite" below). Skipped when the manifest declares no
//     spec.naming.appName, or when there is no manifest.yaml.
//
// Every suite RUNS what it checks rather than merely detecting its
// presence, in the places where that matters (contract, secret-safety,
// teardown, resources) — a rotted conformance test that always passes reports coverage
// that does not exist, which is worse than no test at all. A PARTIAL pass
// is a FAIL: there is no "passed with exceptions", because "skipped" is a
// third, non-failing status reserved for a suite that is honestly not
// applicable here (naming on a manifest that declares no app-name limit),
// never a caveated partial pass.
//
// # The manifest suite
//
// The manifest suite reads the target's manifest.yaml, and the base's, through
// contract.ParseManifest, the kit's one manifest parser. A target the parser
// refuses fails with its reason, as one whose apiVersion is other than
// mitstack/v1alpha or mitstack/v1beta, or missing, does. The parser reads a
// manifest of either version strictly, so a key its version does not name
// fails, naming the key's path. A mitstack/v1beta manifest declares no
// outputs or reads for the registry to resolve. Of a mitstack/v1alpha
// manifest the suite checks four things:
//
//   - A metadata.id, when present and non-empty, must parse through
//     contract.ParseBlockID: a bare slug for a community block, or
//     @<org>/<name> for a private one. A private id must be quoted, as in
//     id: "@acme/billing-db", because YAML does not allow @ at the start of
//     an unquoted value and the platform's parser refuses it. A manifest
//     with no metadata.id is not refused for it. A bad id is reported beside
//     any other problem the suite finds.
//   - Every spec.outputs name must resolve through contract.LookupResource,
//     by name or alias. A name that does not fails, naming the nearest
//     registered name or alias by edit distance.
//   - Every spec.requires[].reads name must be non-empty, resolve through the
//     registry, and name an entry that lists that requirement's capability,
//     refused with the messages the platform gives at apply. A requirement
//     with no reads passes, since operators add their declarations one at a
//     time.
//   - With --base-manifest, a spec.version that differs from the base's
//     needs spec.releaseClass and a non-blank spec.releaseNote. The class
//     value is not checked here; the platform validates it at apply. A base
//     path that does not exist is a block new on this branch, and no version
//     is compared. A base that is present but cannot be read fails. A base
//     the parser refuses leaves its version unknown, so the target needs
//     spec.releaseClass and spec.releaseNote as for a changed version, and
//     a failure names the base and the parser's reason. A pull request that
//     fixes a broken base manifest therefore passes once it carries a
//     release class and note.
//
// The same keys nested anywhere else, under another spec field or inside a
// block scalar, are not read. A manifest.yaml that is present but cannot be
// read, or holds an alias or a merge key anywhere, fails rather than passing
// on what the parser could not see.
//
// On a pull request, publish-block.yml's conformance job writes the base
// commit's manifest.yaml to a temporary file and passes it as
// --base-manifest; other events pass no flag.
//
// # The naming suite
//
// A block that builds provider resource names from the app name declares
// them at spec.naming.appName, read by handler.ManifestAppNameNaming:
//
//	naming:
//	  appName:
//	    maxLength: 27
//	    derivedNames:
//	      - template: "mitstack-app-{appName}-github-actions-ecr-push"
//	        limit: 64
//
// Each template leaves its limit minus the length of its fixed text (the
// template with {appName} removed) for the app name, and maxLength must
// equal the smallest of those. A mismatch fails, naming the template that
// sets the limit and the computed value. A template with no {appName}, a
// template using {appName} more than once, a template whose fixed text alone
// reaches its limit, a maxLength with no derivedNames, and a declaration the
// reader cannot read all fail.
//
// # The resources suite
//
// A block whose mitstack/v1beta manifest declares its resources proves them
// in its own tests: the target's _test.go files must define
// TestDeclaredResources and call kittest.AssertBlock, or the deprecated
// kittest.AssertDeclaredResources. The suite checks both appear, since
// `go test -run` matching nothing passes vacuously, then runs
// `go test -run TestDeclaredResources ./...` in the target, with -v for
// AssertBlock, whose pass detail lists the guards it logged as run: what
// carries the subscription's id matches the declared resources, an observe
// asks the provider, validate names a replaced resource, every declared
// resource observes absent after unsubscribe, and a provided resource
// carries its required outputs. A failure's detail is the first output line naming a resource, as
// resource "<name>" or supplied resource "<name>". A manifest the parser
// refuses fails with its reason.
//
// # Report contract
//
// With --json, Run emits exactly one JSON object on stdout:
//
//	{
//	  "suiteVersion":   "1.3.0",
//	  "kitVersion":     "v0.1.0",
//	  "mode":           "standalone",
//	  "manifestDigest": "sha256:<hex of <target>/manifest.yaml's raw bytes>",
//	  "verdict":        "pass" | "fail",
//	  "resultsDigest":  "sha256:<hex>",
//	  "suites": [
//	    {"name": "trust-plumbing",    "status": "pass"|"fail",          "detail": "..."},
//	    {"name": "contract",          "status": "pass"|"fail",          "detail": "..."},
//	    {"name": "secret-safety",     "status": "pass"|"fail",          "detail": "..."},
//	    {"name": "teardown",          "status": "pass"|"fail",          "detail": "..."},
//	    {"name": "resources",         "status": "pass"|"fail"|"skipped", "detail": "..."},
//	    {"name": "deliverable",       "status": "pass"|"fail",          "detail": "..."},
//	    {"name": "manifest",          "status": "pass"|"fail"|"skipped", "detail": "..."},
//	    {"name": "naming",            "status": "pass"|"fail"|"skipped", "detail": "..."}
//	  ]
//	}
//
// resultsDigest is sha256 over the compact JSON array
// [{"name":...,"status":...}, ...] in run order (detail EXCLUDED, so a
// wording change to a detail message never moves the digest) —
// byte-identical to `jq -c '[.suites[] | {name, status}]' | sha256sum`, so a
// downstream consumer (api/internal/conformance.Mint, which this report
// shape feeds) can recompute and verify it independently. Without --json,
// the same information is rendered as a human-readable suite-by-suite
// report on stdout.
//
// # Exit codes
//
//	0 — the target is fully conformant (verdict "pass")
//	1 — the target failed one or more suites (verdict "fail") — a
//	    target-specific problem, never a usage error
//	2 — a usage error: no --standalone flag, --standalone with no target,
//	    --base-manifest with no path, or any other unrecognised invocation. The message on stdout
//	    always names scripts/block-verify.sh as the tool to use instead for
//	    repo-mode verification.
//
// All output goes through the io.Writer passed to Run — main() itself is
// nothing but `os.Exit(Run(os.Args[1:], os.Stdout))` — so the whole command
// is testable without touching the real process streams.
package main
