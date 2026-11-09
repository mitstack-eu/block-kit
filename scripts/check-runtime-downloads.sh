#!/usr/bin/env bash
# check-runtime-downloads.sh — fail a block whose shipped files name a GitHub release download.
#
# Usage: scripts/check-runtime-downloads.sh <repository>
#
#   exit 0  no tracked file names a releases/download URL, outside test files,
#           testdata/ and the repository's own top-level .github/
#   exit 1  a file does, and each match is named with its file and line, or git
#           could not search the repository
#
# A test file is one named *_test.*, test_*.py or *.tftest.hcl, since none of
# them ships. A .github/ directory below the top level is checked, because a
# block commits what it holds into another repository.
#
# Why this exists. A tool a block fetches from a GitHub release page while it
# runs fails whenever GitHub does, so a block copies the tool from an image named
# by digest with the kit's copy-tool.sh. publish-block.yml's secrets job runs
# this on every block, so no release download comes back.
set -uo pipefail

main() {
  local repo="${1:?usage: check-runtime-downloads.sh <repository>}" found status
  found=$(git -C "$repo" grep --no-color -n -I -F 'releases/download' -- \
    ':(exclude,glob)**/*_test.*' ':(exclude,glob)**/test_*.py' ':(exclude,glob)**/*.tftest.hcl' \
    ':(exclude,glob)**/testdata/**' ':(exclude,glob).github/**')
  status=$?
  if [ "$status" -eq 1 ]; then
    echo "check-runtime-downloads: no shipped file in $repo names a GitHub release download"
    return 0
  fi
  if [ "$status" -ne 0 ]; then
    echo "check-runtime-downloads: git could not search $repo (exit $status)" >&2
    return 1
  fi
  while IFS= read -r match; do
    echo "check-runtime-downloads: $match"
  done <<< "$found"
  echo "check-runtime-downloads: each line above fetches a tool from a GitHub release page; copy it from an image named by digest with the kit's copy-tool.sh instead" >&2
  return 1
}

main "$@"
