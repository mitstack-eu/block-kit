// Command blockprov is the CLI a block's publish workflow calls to build and
// push its provenance record (MIT-3097). It has three subcommands:
//
//	blockprov build   — reads a blockverify --json report and a manifest,
//	                    refuses anything short of a "pass" verdict, mints the
//	                    record's OIDC token from GitHub's own token endpoint,
//	                    and writes the signed record to --out.
//	blockprov verify  — runs the same provenance.Verify code path the
//	                    platform calls, exposed so a human (or a workflow
//	                    step) can check a record by hand.
//	blockprov push    — writes a built record into a registry beside its
//	                    image, over the plain OCI distribution API.
//
// See package provenance for the record shape, the signing mechanism and why
// it was chosen over Sigstore keyless, and for why the pushed manifest is a
// tagged artifact with no OCI subject.
//
// # build
//
// build's own refusal is the second gate over the same fact: a red
// conformance run should already have stopped a workflow_call caller's
// publish job from starting at all (needs: conformance), but build checks
// the report's verdict itself too, so a workflow that somehow reaches this
// command with a stale or hand-edited failing report still cannot mint a
// record for it. On refusal it writes nothing to --out and exits 1 without
// ever requesting a token — a failed run must never even ask GitHub to sign
// anything.
//
// # push
//
// push speaks the OCI distribution API directly with net/http and HTTP
// Basic auth — no registry client dependency, matching the kit's
// stdlib-only rule. A blob (the record bytes, and the empty OCI config blob)
// goes up through the two-step upload every OCI registry implements: POST
// .../blobs/uploads/ to start (a 202 with a Location header naming the
// upload session), then PUT that location with ?digest=<sha256> to
// complete it. The manifest itself is then PUT under the tag
// sha256-<hex-of-image-digest>.prov, carrying artifactType and no subject
// descriptor — see package provenance's doc comment for why a referrer would
// not survive the platform's own untagged-image lifecycle policy.
//
// --registry accepts either a bare host (assumed https) or a full
// scheme+host (used as given, which is what lets a test point it at a
// plain-http httptest.NewServer).
//
// Exactly one of --password or --password-env is required. --password-env
// names an environment variable to read the credential from instead of
// taking it as an argument, so a live registry password never sits in argv
// where /proc or a stray `set -x` could expose it — the way a CI step should
// always pass it.
//
// # Exit codes
//
// 0 success, 1 a real failure (a rejected record, an unreachable registry, a
// non-pass verdict), 2 a usage error — a missing subcommand or a missing
// required flag — matching blockverify's own 0/1/2 contract. All output
// goes through the io.Writer passed to Run; main() itself is nothing but
// os.Exit(Run(os.Args[1:], os.Stdout)).
package main
