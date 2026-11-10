#!/usr/bin/env bash
# release-tag.sh — guards and cuts a block's release tag at the commit publish-block.yml built, and starts the block's publisher at it.
#
# Usage:
#   scripts/release-tag.sh check <tag>
#   scripts/release-tag.sh push <tag>
#   scripts/release-tag.sh dispatch <tag> <publisher-workflow>
#
# A release is a tag v<version> at the commit its image was built from, and a
# tag never moves. check fails when <tag> already names a commit other than
# GITHUB_SHA. push creates <tag> at GITHUB_SHA unless it already names it.
# dispatch runs .github/workflows/<publisher-workflow> at <tag> when the
# block's repository holds that file, because a tag pushed with the job's
# token starts no workflow run. It reads GITHUB_API_URL, GITHUB_REPOSITORY and
# GITHUB_SHA from the runner and the job's token from GITHUB_TOKEN, so no
# credential reaches a checkout, and calls GitHub with curl, since a
# self-hosted runner may hold no gh.
set -euo pipefail

usage() {
  echo "usage: release-tag.sh check <tag> | push <tag> | dispatch <tag> <publisher-workflow>" >&2
  exit 2
}

# github calls the GitHub REST API, writes the answer's body to the file out
# names and prints its HTTP status, which is 000 when no answer came.
github() {
  local method=$1 path=$2 out=$3 body=${4:-}
  local args=(--silent --show-error --request "$method" --output "$out" --write-out '%{http_code}'
    --header "Authorization: Bearer ${GITHUB_TOKEN}" --header "Accept: application/vnd.github+json")
  if [ -n "$body" ]; then
    args+=(--header "Content-Type: application/json" --data "$body")
  fi
  curl "${args[@]}" "${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/${path}" || true
}

# refuse_answer fails naming what could not be done and GitHub's answer.
refuse_answer() {
  local what=$1 status=$2 out=$3
  echo "::error::could not ${what}: GitHub answered HTTP ${status}: $(cat "$out")" >&2
  rm -f "$out"
  exit 1
}

# tagged_commit prints the commit tag names, and nothing when the repository
# holds no such tag.
tagged_commit() {
  local tag=$1 out status sha
  out=$(mktemp)
  status=$(github GET "git/ref/tags/${tag}" "$out")
  case "$status" in
    200) ;;
    404)
      rm -f "$out"
      return 0
      ;;
    *) refuse_answer "read whether ${GITHUB_REPOSITORY} holds tag ${tag}" "$status" "$out" ;;
  esac
  sha=$(jq -r .object.sha "$out")
  # An annotated tag names a tag object, which in turn names the commit.
  if [ "$(jq -r .object.type "$out")" = "tag" ]; then
    status=$(github GET "git/tags/${sha}" "$out")
    [ "$status" = 200 ] || refuse_answer "read the commit the annotated tag ${tag} names" "$status" "$out"
    sha=$(jq -r .object.sha "$out")
  fi
  rm -f "$out"
  echo "$sha"
}

refuse_moving() {
  local tag=$1 held=$2
  echo "::error::${tag} already names commit ${held}, and this run built commit ${GITHUB_SHA}. A tag never moves, so raise spec.version to release this commit." >&2
  exit 1
}

check() {
  local tag=$1 held
  held=$(tagged_commit "$tag")
  case "$held" in
    "") echo "${tag} is not cut yet; this run cuts it at ${GITHUB_SHA} once the image and its record are pushed" ;;
    "$GITHUB_SHA") echo "${tag} already names this commit, ${GITHUB_SHA}" ;;
    *) refuse_moving "$tag" "$held" ;;
  esac
}

push() {
  local tag=$1 held out status
  held=$(tagged_commit "$tag")
  case "$held" in
    "$GITHUB_SHA")
      echo "${tag} already names this commit, ${GITHUB_SHA}, so nothing is pushed"
      return 0
      ;;
    "") ;;
    *) refuse_moving "$tag" "$held" ;;
  esac
  out=$(mktemp)
  status=$(github POST "git/refs" "$out" "$(jq -cn --arg ref "refs/tags/${tag}" --arg sha "$GITHUB_SHA" '{ref: $ref, sha: $sha}')")
  [ "$status" = 201 ] || refuse_answer "cut ${tag} at ${GITHUB_SHA}" "$status" "$out"
  rm -f "$out"
  echo "cut ${tag} at ${GITHUB_SHA}"
}

dispatch() {
  local tag=$1 publisher=$2 out status
  if [ ! -f ".github/workflows/${publisher}" ]; then
    echo "the block holds no .github/workflows/${publisher}, so no publisher is started for ${tag}"
    return 0
  fi
  out=$(mktemp)
  status=$(github POST "actions/workflows/${publisher}/dispatches" "$out" "$(jq -cn --arg tag "$tag" '{ref: $tag, inputs: {tag: $tag}}')")
  [ "$status" = 204 ] || refuse_answer "start ${publisher} at ${tag}" "$status" "$out"
  rm -f "$out"
  echo "started ${publisher} at ${tag}"
}

case "${1:-}" in
  check | push)
    [ "$#" -eq 2 ] || usage
    "$1" "$2"
    ;;
  dispatch)
    [ "$#" -eq 3 ] || usage
    dispatch "$2" "$3"
    ;;
  *) usage ;;
esac
