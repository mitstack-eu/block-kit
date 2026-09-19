#!/usr/bin/env bash
# check-release-workflow_test.sh — a structural guard over
# .github/workflows/ci.yml (MIT-3068, ADR 0068 decision 5).
#
# This is a self-contained test, not a checker-script-plus-test pair, the
# same shape as scripts/pin-bump-workflow_test.sh and
# scripts/go-lint-step_test.sh over there: it judges the committed workflow
# file directly and reports pass/fail on its own.
#
# CONTRACT this file assumes of .github/workflows/ci.yml (written by
# code-author, not this file):
#
#   on: pull_request (against main) and push to main.
#
#   jobs.test runs on every pull_request and every push to main, on
#   ${{ fromJSON(vars.CI_RUNS_ON || '["ubicloud-standard-2"]') }}, with
#   actions/checkout and actions/setup-go pinned to the same shas the
#   platform's own pipeline.yml pins (actions/checkout@3d3c42e5aac5ba805825
#   da76410c181273ba90b1, actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef
#   454303e, go-version '1.26.8'), and runs `go vet ./...` and
#   `go test ./...`.
#
#   jobs.release runs only on push to main, only after jobs.test (needs:
#   test, and an `if:` naming both refs/heads/main and the push event), with
#   permissions: contents: write, a checkout step with fetch-depth: 0, a
#   shell: bash default, a job-level concurrency block (group: kit-release,
#   cancel-in-progress: false), and a step running exactly
#   `bash scripts/release-operator-kit.sh | tee -a "$GITHUB_STEP_SUMMARY"`.
#
# Parsed the same way scripts/check-ci-no-cancel-main.sh reads
# pipeline.yml's concurrency block: plain awk section extraction by
# indentation, then grep on the extracted block, composed to reach a nested
# key (jobs -> release -> concurrency). No yq, no PyYAML — the file is judged
# by structure well enough with the tools every runner already has.
#
# Run: bash scripts/check-release-workflow_test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/ci.yml"

pass=0
fail=0
ok()  { pass=$((pass + 1)); printf 'ok   — %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL — %s\n' "$1"; }

if [ ! -f "$WORKFLOW" ]; then
  bad "$WORKFLOW exists to be judged (file not found)"
  echo
  echo "check-release-workflow: $pass passed, $fail failed"
  exit 1
fi
ok "$WORKFLOW is present"

# block <text> <key> — the lines nested under "<key>:" inside <text>,
# regardless of its own indentation: every subsequent line more indented than
# the key line, up to (but excluding) the first later non-blank line at or
# above the key's own indent. Indentation is read from the line itself rather
# than assumed, so this composes: the result of one call (still carrying its
# original absolute indentation) can be fed into another to reach a nested
# key, the way jobs -> release -> concurrency is read below. Mirrors the
# column-0 concurrency-block extraction in check-ci-no-cancel-main.sh, made
# indent-generic so it also works one and two levels deeper.
block() {
  awk -v key="$2" '
    {
      match($0, /^[ ]*/)
      indent = RLENGTH
    }
    !found && $0 ~ ("^[ ]*" key ":") { keyindent = indent; found = 1; next }
    found && $0 !~ /^[ ]*$/ && indent <= keyindent { found = 0 }
    found { print }
  ' <<<"$1"
}

workflow_text="$(cat "$WORKFLOW")"
jobs_block="$(block "$workflow_text" jobs)"

# --- triggers ----------------------------------------------------------------

on_block="$(block "$workflow_text" on)"
if [ -z "$on_block" ]; then
  bad "workflow has a top-level on: block"
else
  ok "workflow has a top-level on: block"
fi

if grep -q 'pull_request:' <<<"$on_block"; then
  ok "on: includes pull_request"
else
  bad "on: is missing pull_request"
fi

if grep -qE '^\s*push:' <<<"$on_block" && grep -q 'main' <<<"$on_block"; then
  ok "on: includes push (scoped to main)"
else
  bad "on: is missing a push trigger scoped to main"
fi

# --- jobs.test -----------------------------------------------------------

test_block="$(block "$jobs_block" test)"
if [ -z "$test_block" ]; then
  bad "jobs.test is present"
else
  ok "jobs.test is present"
fi

if grep -qF 'fromJSON(vars.CI_RUNS_ON' <<<"$test_block" && grep -qF 'ubicloud-standard-2' <<<"$test_block"; then
  ok "jobs.test.runs-on reads vars.CI_RUNS_ON with the ubicloud-standard-2 fallback"
else
  bad "jobs.test.runs-on does not read vars.CI_RUNS_ON with the ubicloud-standard-2 fallback"
fi

if grep -qF 'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1' <<<"$test_block"; then
  ok "jobs.test pins actions/checkout to the platform's sha"
else
  bad "jobs.test does not pin actions/checkout to the platform's sha (3d3c42e5aac5ba805825da76410c181273ba90b1)"
fi

if grep -qF 'actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e' <<<"$test_block"; then
  ok "jobs.test pins actions/setup-go to the platform's sha"
else
  bad "jobs.test does not pin actions/setup-go to the platform's sha (b7ad1dad31e06c5925ef5d2fc7ad053ef454303e)"
fi

if grep -qF "go-version: '1.26.8'" <<<"$test_block"; then
  ok "jobs.test pins go-version to 1.26.8"
else
  bad "jobs.test does not pin go-version to '1.26.8'"
fi

if grep -qE '(^|[^./])go vet \./\.\.\.' <<<"$test_block"; then
  ok "jobs.test runs go vet ./..."
else
  bad "jobs.test does not run go vet ./..."
fi

if grep -qE '(^|[^./])go test \./\.\.\.' <<<"$test_block"; then
  ok "jobs.test runs go test ./..."
else
  bad "jobs.test does not run go test ./..."
fi

# --- jobs.release ----------------------------------------------------------

release_block="$(block "$jobs_block" release)"
if [ -z "$release_block" ]; then
  bad "jobs.release is present"
else
  ok "jobs.release is present"
fi

if grep -qE '^\s*needs:\s*test\s*$' <<<"$release_block"; then
  ok "jobs.release needs: test"
else
  bad "jobs.release does not declare needs: test"
fi

if_line="$(grep -E '^\s*if:' <<<"$release_block" | head -1)"
if grep -qF "refs/heads/main" <<<"$if_line" && grep -qF "github.event_name == 'push'" <<<"$if_line"; then
  ok "jobs.release runs only on push to main"
else
  bad "jobs.release's if: does not gate on both refs/heads/main and github.event_name == 'push' (got: ${if_line:-<none>})"
fi

perm_block="$(block "$release_block" permissions)"
if grep -qE '^\s*contents:\s*write\s*$' <<<"$perm_block"; then
  ok "jobs.release.permissions grants contents: write"
else
  bad "jobs.release.permissions does not grant contents: write"
fi

concurrency_block="$(block "$release_block" concurrency)"
if grep -qE '^\s*group:\s*kit-release\s*$' <<<"$concurrency_block"; then
  ok "jobs.release.concurrency.group is kit-release"
else
  bad "jobs.release.concurrency.group is not kit-release"
fi
if grep -qE '^\s*cancel-in-progress:\s*false\s*$' <<<"$concurrency_block"; then
  ok "jobs.release.concurrency.cancel-in-progress is false"
else
  bad "jobs.release.concurrency.cancel-in-progress is not false"
fi

if grep -qE '^\s*shell:\s*bash\s*$' <<<"$release_block"; then
  ok "jobs.release sets shell: bash"
else
  bad "jobs.release does not set shell: bash"
fi

if grep -qF 'fetch-depth: 0' <<<"$release_block"; then
  ok "jobs.release's checkout uses fetch-depth: 0"
else
  bad "jobs.release's checkout does not use fetch-depth: 0"
fi

if grep -qF 'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1' <<<"$release_block"; then
  ok "jobs.release pins actions/checkout to the platform's sha"
else
  bad "jobs.release does not pin actions/checkout to the platform's sha (3d3c42e5aac5ba805825da76410c181273ba90b1)"
fi

if grep -qF 'bash scripts/release-operator-kit.sh | tee -a "$GITHUB_STEP_SUMMARY"' <<<"$release_block"; then
  ok "jobs.release runs bash scripts/release-operator-kit.sh | tee -a \"\$GITHUB_STEP_SUMMARY\""
else
  bad "jobs.release does not run bash scripts/release-operator-kit.sh | tee -a \"\$GITHUB_STEP_SUMMARY\""
fi

echo
echo "check-release-workflow: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
