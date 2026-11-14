#!/usr/bin/env bash
# run-rule-tests.sh — run a block's alert rule tests with promtool 3.16.0,
# checked by sha256.
#
# Usage: scripts/run-rule-tests.sh <block>
#
#   exit 0  every test file under <block>/testdata/rules/ passes, or the block
#           has no testdata/rules/, in which case no promtool is fetched
#   exit 1  promtool failed a test, naming its group and alert, or refused a
#           rule file; a test names a rule file the block does not hold;
#           testdata/rules/ holds no *.yaml test file, or a file that is not a
#           *.yaml file at its top; or the pinned promtool could not be run
#
# A test file is a promtool unit test file, testdata/rules/*.yaml. Each file
# under rules/<kind>/ holds one bare rule group, the body a Cockpit's ruler
# takes, while promtool reads only a file whose top level lists its groups.
# So the script copies each group to <kind>/<file> in a scratch directory,
# indented unchanged under `groups:`, puts the test files beside them and runs
# promtool there. A test's rule_files names metrics/<file>, and promtool judges
# the bytes the block posts. Nothing is written into the block's tree.
#
# Why this exists. Both conformance jobs of publish-block.yml run it on every
# event, so a block's alert rules fire on made-up signals before any Cockpit
# holds them, with one promtool pin for every block.
set -euo pipefail

readonly PROMTOOL_VERSION=3.16.0
readonly PROMTOOL_SHA256=a3c953f444da51b0f1198a11e136715a522bbe003c288b7c27e5007be3780267
readonly PROMTOOL_URL="https://github.com/prometheus/prometheus/releases/download/v${PROMTOOL_VERSION}/prometheus-${PROMTOOL_VERSION}.linux-amd64.tar.gz"

# shellcheck source=scripts/pinned-tool.sh
. "$(dirname "${BASH_SOURCE[0]}")/pinned-tool.sh"

# copy_groups <block> <scratch> writes each rule group as the one item of a groups list.
copy_groups() {
  local file kind
  for file in "$1"/rules/*/*.yaml; do
    [ -f "$file" ] || continue
    kind=$(basename "$(dirname "$file")")
    mkdir -p "$2/$kind"
    { echo 'groups:'; sed '1s/^/  - /; 2,$s/^/    /' "$file"; } > "$2/$kind/$(basename "$file")"
  done
}

main() {
  local block="${1:?usage: run-rule-tests.sh <block>}" promtool status=0 skipped
  if [ ! -d "$block/testdata/rules" ]; then
    echo "run-rule-tests: $block holds no rule test under testdata/rules/, so promtool does not run"
    return 0
  fi
  skipped=$(find "$block/testdata/rules" -type f \( -path "$block/testdata/rules/*/*" -o ! -name '*.yaml' \))
  if [ -n "$skipped" ]; then
    sed 's|^|run-rule-tests: |; s|$| is no *.yaml test file at the top of testdata/rules/, so promtool would skip it|' <<<"$skipped" >&2
    return 1
  fi
  local -a tests=("$block"/testdata/rules/*.yaml)
  if [ ! -f "${tests[0]}" ]; then
    echo "run-rule-tests: $block/testdata/rules/ holds no *.yaml test file, so promtool would test nothing" >&2
    return 1
  fi
  promtool=$(pinned_tool promtool "$PROMTOOL_VERSION" "$PROMTOOL_URL" "prometheus-${PROMTOOL_VERSION}.linux-amd64/promtool" "$PROMTOOL_SHA256")
  scratch=$(mktemp -d)
  trap 'rm -rf "$scratch"' EXIT
  copy_groups "$block" "$scratch"
  cp "${tests[@]}" "$scratch/"
  (cd "$scratch" && "$promtool" test rules "${tests[@]##*/}") > "$scratch/promtool.out" 2>&1 || status=$?
  cat "$scratch/promtool.out"
  # promtool only warns of a rule_files entry matching no file, and a test expecting no alert then passes.
  if grep -q 'no file match pattern' "$scratch/promtool.out"; then
    echo "run-rule-tests: a rule test of $block names a rule file the block does not hold, in the WARNING above" >&2
    return 1
  fi
  if [ "$status" -ne 0 ]; then
    echo "run-rule-tests: promtool $PROMTOOL_VERSION failed a rule test of $block, named above" >&2
    return 1
  fi
  echo "run-rule-tests: promtool $PROMTOOL_VERSION passed ${#tests[@]} rule test file(s) of $block"
}

main "$@"
