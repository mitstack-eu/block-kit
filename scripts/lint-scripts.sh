#!/usr/bin/env bash
# lint-scripts.sh — run shellcheck 0.11.0, checked by sha256, over every shell
# script a repository tracks.
#
# Usage: scripts/lint-scripts.sh <repository>
#
#   exit 0  every tracked shell script is clean, or the repository tracks none
#   exit 1  shellcheck reported a finding, naming its file, line and SC code,
#           or the pinned shellcheck could not be run
#
# A shell script is a tracked file named *.sh or *.bash, or one whose first
# line is a shebang naming sh, bash, dash or ksh.
#
# Why this exists. publish-block.yml's scripts job runs it on every block, and
# block-kit's tests run it on throwaway repositories, so the job and its proof
# run one lint.
set -euo pipefail

readonly SHELLCHECK_VERSION=0.11.0
readonly SHELLCHECK_SHA256=4da528ddb3a4d1b7b24a59d4e16eb2f5fd960f4bd9a3708a15baddbdf1d5a55b
readonly SHELLCHECK_URL="https://github.com/koalaman/shellcheck/releases/download/v${SHELLCHECK_VERSION}/shellcheck-v${SHELLCHECK_VERSION}.linux.x86_64.tar.gz"

# shellcheck source=scripts/pinned-tool.sh
. "$(dirname "${BASH_SOURCE[0]}")/pinned-tool.sh"

is_shell_script() {
  local path="$1" first=""
  case "$path" in
    *.sh | *.bash) return 0 ;;
  esac
  [ -f "$path" ] || return 1
  IFS= read -r first < "$path" 2>/dev/null || true
  [[ "$first" =~ ^\#!.*[/[:space:]](ba|da|k)?sh([[:space:]]|$) ]]
}

main() {
  local repo="${1:?usage: lint-scripts.sh <repository>}" shellcheck tracked path
  local -a scripts=()
  shellcheck=$(pinned_tool shellcheck "$SHELLCHECK_VERSION" "$SHELLCHECK_URL" "shellcheck-v${SHELLCHECK_VERSION}/shellcheck" "$SHELLCHECK_SHA256")
  cd "$repo"
  # A file, not a process substitution, so a failing git ls-files fails the lint.
  tracked=$(mktemp)
  git ls-files -z > "$tracked"
  while IFS= read -r -d '' path; do
    if is_shell_script "$path"; then
      scripts+=("$path")
    fi
  done < "$tracked"
  rm -f "$tracked"
  if [ "${#scripts[@]}" -eq 0 ]; then
    echo "lint-scripts: $repo tracks no shell script"
    return 0
  fi
  echo "lint-scripts: shellcheck $SHELLCHECK_VERSION over ${#scripts[@]} tracked script(s)"
  "$shellcheck" -- "${scripts[@]}"
}

main "$@"
