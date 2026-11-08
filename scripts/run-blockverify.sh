#!/usr/bin/env bash
# run-blockverify.sh — runs blockverify on the block's tree, prints its report and fails on any verdict but pass.
#
# Usage: scripts/run-blockverify.sh <blockverify> <report> <event> <pr-dir> [blockverify flag ...]
#
# Both conformance jobs of publish-block.yml judge a block this way. On a
# pull_request event it passes the base manifest and changed files that
# write-pr-base.sh wrote to <pr-dir>, which a push has no base commit for.
# The report prints before the verdict decides, so a failing run's log names
# what failed.
set -euo pipefail

if [ "$#" -lt 4 ]; then
  echo "usage: run-blockverify.sh <blockverify> <report> <event> <pr-dir> [blockverify flag ...]" >&2
  exit 2
fi
blockverify=$1 report=$2 event=$3 pr_dir=$4
shift 4
flags=("$@")
if [ "$event" = "pull_request" ]; then
  flags+=(--base-manifest "$pr_dir/base-manifest.yaml" --changed-files "$pr_dir/changed-files")
fi

status=0
"$blockverify" --standalone --json "${flags[@]}" . > "$report" || status=$?
cat "$report"
verdict=$(jq -r .verdict "$report")
if [ "$status" -ne 0 ] || [ "$verdict" != "pass" ]; then
  echo "blockverify exited $status with verdict '$verdict', expected 'pass'" >&2
  exit 1
fi
