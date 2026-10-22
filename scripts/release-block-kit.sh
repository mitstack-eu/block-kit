#!/usr/bin/env bash
# release-block-kit.sh — decide whether a pushed commit range changed this
# repository, and cut the next kit tag at head when it did.
#
# Explained in the root README.md, "Release".
#
# Usage:
#   scripts/release-block-kit.sh [--base <sha>] [--head <sha>] [--repo <dir>]
#                                   [--remote <name>] [--dry-run] [--verify-only]
#
#   --base <sha>     the push's "before" commit. An empty value, the all-zero
#                    sha, and omitting the flag all mean unknown, and the base
#                    is then resolved from the newest kit tag.
#   --head <sha>     the commit a tag is cut at (default HEAD).
#   --repo <dir>     the repository every git call runs in (default: the
#                    repository holding this script).
#   --remote <name>  where a cut tag is pushed (default origin).
#   --dry-run        decide and report, create and push nothing.
#   --verify-only    assert head is released, cut nothing.
#
#   exit 0  the kit is unchanged, head already carries a kit tag, a tag was cut
#           and verified, or a plan was reported
#   exit 1  head is unreachable from any kit tag after the cut, a --verify-only
#           assertion did not hold, or the computed tag is already published
#           elsewhere
#   exit 2  an unknown flag
#
# Why this exists. This repository IS the kit: github.com/mitstack-eu/block-kit
# is a root Go module, and a Go consumer resolves it only through a plain
# vX.Y.Z tag such as v0.5.1. A change that reaches main without such a tag is
# unreachable to every block repository importing the kit, however green its
# delivery looked.
# Resolving the base from the newest kit tag rather than from the push's
# before sha is what makes a skipped or red run recoverable: the next push
# cuts the release the lost run owed, even when that push touched nothing
# itself.
set -uo pipefail

KIT_PATH="."
TAG_PREFIX="v"
ZERO_SHA="0000000000000000000000000000000000000000"

base=""
head_ref="HEAD"
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
remote="origin"
dry_run=0
verify_only=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base)   base="${2-}";     shift $(( $# > 1 ? 2 : 1 )) ;;
    --head)   head_ref="${2-}"; shift $(( $# > 1 ? 2 : 1 )) ;;
    --repo)   repo="${2-}";     shift $(( $# > 1 ? 2 : 1 )) ;;
    --remote) remote="${2-}";   shift $(( $# > 1 ? 2 : 1 )) ;;
    --dry-run)     dry_run=1;     shift ;;
    --verify-only) verify_only=1; shift ;;
    *)
      echo "release-block-kit: unknown flag: $1" >&2
      echo "usage: release-block-kit.sh [--base <sha>] [--head <sha>] [--repo <dir>] [--remote <name>] [--dry-run] [--verify-only]" >&2
      exit 2
      ;;
  esac
done

# Every git call goes through the chosen repository, so the script never depends
# on the directory it happens to be invoked from.
in_repo() { git -C "$repo" "$@"; }

# The outcome the calling workflow step reads. Written only when the runner
# provides the file, and never carrying both released values for one run.
emit_output() {
  [ -n "${GITHUB_OUTPUT:-}" ] || return 0
  printf '%s\n' "$@" >> "$GITHUB_OUTPUT"
}

# Kit tags, local only: the version scan must not fetch. A fetch would pull in a
# tag somebody else published, compute the version after it, and quietly cut a
# release on top of a tree this run has never seen. Anything not shaped
# vX.Y.Z is ignored.
kit_versions() {
  in_repo tag --list "$TAG_PREFIX*" 2> /dev/null \
    | grep -E "^$TAG_PREFIX[0-9]+\.[0-9]+\.[0-9]+$" \
    | sed "s|^$TAG_PREFIX||"
}

# The highest kit version by NUMERIC order on each level, so v0.10.0 beats
# v0.9.0. A lexical sort picks the wrong base the first time a minor reaches ten.
newest_kit_version() {
  kit_versions | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1
}

head_sha="$(in_repo rev-parse --verify --quiet "${head_ref}^{commit}" 2> /dev/null)"
if [ -z "$head_sha" ]; then
  echo "release-block-kit: cannot resolve --head '$head_ref' in $repo" >&2
  emit_output "released=false"
  exit 1
fi

newest_version="$(newest_kit_version)"
newest_tag=""
[ -n "$newest_version" ] && newest_tag="$TAG_PREFIX$newest_version"

# Base resolution. An explicit, resolvable --base wins; anything else falls back
# to the newest kit tag's commit, then to head's parent, then to no base at all.
resolved_base=""
if [ -n "$base" ] && [ "$base" != "$ZERO_SHA" ]; then
  resolved_base="$(in_repo rev-parse --verify --quiet "${base}^{commit}" 2> /dev/null)"
fi
if [ -z "$resolved_base" ] && [ -n "$newest_tag" ]; then
  resolved_base="$(in_repo rev-parse --verify --quiet "refs/tags/$newest_tag^{commit}" 2> /dev/null)"
fi
if [ -z "$resolved_base" ]; then
  resolved_base="$(in_repo rev-parse --verify --quiet "${head_sha}^^{commit}" 2> /dev/null)"
fi

# With no base at all — no kit tag and a head with no parent — the whole tree is
# new, so the kit counts as changed and no diff is computed.
if [ -n "$resolved_base" ]; then
  touched="$(in_repo diff --name-only "$resolved_base" "$head_sha" -- "$KIT_PATH" 2> /dev/null)"
  range="$resolved_base..$head_sha"
else
  touched="$KIT_PATH"
  range="$head_sha"
fi

echo "release-block-kit: range $range (base ${resolved_base:-<none>}, head $head_sha)"

if [ -z "$touched" ]; then
  echo "the repository is unchanged in this range, so no kit tag is cut."
  emit_output "released=false"
  exit 0
fi

# One push cuts at most one tag: a kit tag that already contains head has
# released this tree, whatever else the range touched.
containing="$(in_repo tag --list "$TAG_PREFIX*" --contains "$head_sha" 2> /dev/null)"
if [ -n "$containing" ]; then
  echo "the repository changed, and $head_sha is already released by: $(tr '\n' ' ' <<< "$containing")"
  emit_output "released=false"
  exit 0
fi

if [ "$verify_only" -eq 1 ]; then
  echo "::error::the repository changed in $range and no $TAG_PREFIX* tag contains $head_sha, so a consumer cannot resolve the module at this commit." >&2
  emit_output "released=false"
  exit 1
fi

# The raise level. A whole line in any commit message in the range asks for one;
# major wins over minor, and each zeroes the levels below it.
level="patch"
messages="$(in_repo log --format=%B "$range" 2> /dev/null)"
if grep -qiE '^[[:space:]]*kit-release:[[:space:]]*major[[:space:]]*$' <<< "$messages"; then
  level="major"
elif grep -qiE '^[[:space:]]*kit-release:[[:space:]]*minor[[:space:]]*$' <<< "$messages"; then
  level="minor"
fi

IFS=. read -r cur_major cur_minor cur_patch <<< "${newest_version:-0.0.0}"
case "$level" in
  major) next_version="$(( cur_major + 1 )).0.0" ;;
  minor) next_version="$cur_major.$(( cur_minor + 1 )).0" ;;
  *)     next_version="$cur_major.$cur_minor.$(( cur_patch + 1 ))" ;;
esac
next_tag="$TAG_PREFIX$next_version"

echo "the repository changed in $range: a $level raise from ${newest_tag:-<no kit tag>} gives $next_tag."

if [ "$dry_run" -eq 1 ]; then
  echo "dry run: would cut $next_tag at $head_sha and push it to $remote."
  emit_output "released=false"
  exit 0
fi

# Every block importing this module resolves it by tag, so a name already
# published at another commit fails the run rather than moving. Nothing here
# forces a ref.
published="$(in_repo ls-remote --tags "$remote" "refs/tags/$next_tag" 2> /dev/null)"
if [ -n "$published" ]; then
  if grep -qF "$head_sha" <<< "$published"; then
    echo "$next_tag is already published at $head_sha, so there is nothing to cut."
    emit_output "released=false"
    exit 0
  fi
  echo "::error::$next_tag already exists on $remote at another commit, so this run cuts nothing rather than moving a tag the block repositories resolve." >&2
  emit_output "released=false"
  exit 1
fi

if ! in_repo tag "$next_tag" "$head_sha"; then
  echo "::error::cannot create $next_tag at $head_sha." >&2
  emit_output "released=false"
  exit 1
fi

# An unforced push, so a tag that appeared on the remote since the check above
# is rejected rather than overwritten. The local tag goes with it, because it
# names a release that was never published.
if ! in_repo push "$remote" "refs/tags/$next_tag"; then
  in_repo tag -d "$next_tag" > /dev/null 2>&1
  echo "::error::$remote refused $next_tag, so the published tag stays where it is." >&2
  emit_output "released=false"
  exit 1
fi

released_by="$(in_repo tag --list "$TAG_PREFIX*" --contains "$head_sha" 2> /dev/null)"
if [ -z "$released_by" ]; then
  echo "::error::$next_tag was pushed and no tag contains $head_sha, so the kit stays unreachable to its consumers." >&2
  emit_output "released=false"
  exit 1
fi

echo "cut $next_tag at $head_sha and pushed it to $remote."
emit_output "released=true" "tag=$next_tag"
exit 0
