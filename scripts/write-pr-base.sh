#!/usr/bin/env bash
# write-pr-base.sh — writes a pull request's base manifest and the files its merge changes, the two inputs of blockverify's version rule.
#
# Usage: [FETCH_TOKEN=<token>] scripts/write-pr-base.sh <base-sha> <out-dir>
#
# Both conformance jobs of publish-block.yml run it in the block's checkout,
# whose HEAD is the merge commit, so one script writes what each judges.
# A base with no manifest.yaml writes none, which blockverify reads as a block
# new on this branch. FETCH_TOKEN authenticates the fetch of the base commit
# through git's environment, for a checkout that keeps no credential.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: [FETCH_TOKEN=<token>] write-pr-base.sh <base-sha> <out-dir>" >&2
  exit 2
fi
base=$1 out=$2

fetch_base() {
  if [ -z "${FETCH_TOKEN:-}" ]; then
    git fetch --depth=1 origin "$base"
    return
  fi
  AUTH_HEADER="AUTHORIZATION: basic $(printf 'x-access-token:%s' "$FETCH_TOKEN" | base64 -w0)" \
    git --config-env=http.extraheader=AUTH_HEADER fetch --depth=1 origin "$base"
}

fetch_base
if git cat-file -e "$base:manifest.yaml" 2>/dev/null; then
  git show "$base:manifest.yaml" > "$out/base-manifest.yaml"
else
  echo "the base commit $base has no manifest.yaml, so the block is new on this branch"
fi
git diff --no-renames -z --name-only "$base" HEAD > "$out/changed-files"
