#!/usr/bin/env bash
# base-images.sh — hands the private images a block's Dockerfile builds on from the job that may pull them to the job that builds.
#
# Usage:
#   scripts/base-images.sh pull <registry> <dir>
#   scripts/base-images.sh load <dir>
#
# An OpenTofu-only block builds on the operator image, which publish-block.yml
# pulls with an AWS role, and assuming that role takes GitHub's identity token.
# No job holding that token may run the block's code, so the pull and the
# build are two jobs. pull reads the Dockerfile at GITHUB_SHA through the
# GitHub API with GITHUB_TOKEN, as text and with no checkout, pulls every
# image reference it names in <registry>, and saves them to <dir> beside
# the list of references. load loads them where the image is built and prints
# a docker buildx --build-context flag for each, one argument a line, so the
# build reads each reference from the local store and needs no registry login.
set -euo pipefail

usage() {
  echo "usage: base-images.sh pull <registry> <dir> | load <dir>" >&2
  exit 2
}

dockerfile() {
  local out=$1 status
  status=$(curl --silent --show-error --output "$out" --write-out '%{http_code}' \
    --header "Authorization: Bearer ${GITHUB_TOKEN}" --header "Accept: application/vnd.github.raw+json" \
    "${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/contents/Dockerfile?ref=${GITHUB_SHA}" || true)
  if [ "$status" != 200 ]; then
    echo "::error::could not read the Dockerfile of ${GITHUB_REPOSITORY} at ${GITHUB_SHA}: GitHub answered HTTP ${status}: $(cat "$out")" >&2
    exit 1
  fi
}

pull() {
  local registry=$1 dir=$2 work ref tag tags=()
  local whole="^${registry//./\\.}/[A-Za-z0-9._/-]+(:[A-Za-z0-9._-]+)?(@sha256:[0-9a-f]{64})?$"
  mkdir -p "$dir"
  work=$(mktemp -d)
  dockerfile "$work/Dockerfile"
  # Every word that starts with the registry, outside a comment line, with
  # = and quotes read as spaces.
  awk -v prefix="${registry}/" '!/^[[:space:]]*#/ {
    count = split($0, words, /[[:space:]="\047]+/)
    for (i = 1; i <= count; i++) if (index(words[i], prefix) == 1) print words[i]
  }' "$work/Dockerfile" | sort -u > "$work/names"
  # A name a variable completes would be pulled under another name than the
  # build asks for.
  while read -r ref; do
    if [[ ! $ref =~ $whole ]]; then
      echo "::error::the Dockerfile of ${GITHUB_REPOSITORY} names ${ref}, which is not a whole image reference. Write the name with its tag or digest in full, with no variable in it." >&2
      exit 1
    fi
  done < "$work/names"
  : > "$dir/references"
  while read -r ref; do
    tag="base-image:${#tags[@]}"
    docker pull "$ref"
    docker tag "$ref" "$tag"
    echo "$ref $tag" >> "$dir/references"
    tags+=("$tag")
  done < "$work/names"
  rm -rf "$work"
  if [ "${#tags[@]}" -eq 0 ]; then
    echo "::warning::the Dockerfile of ${GITHUB_REPOSITORY} names no image in ${registry}, so the build is handed none and can pull only what needs no login"
    return 0
  fi
  docker save --output "$dir/images.tar" "${tags[@]}"
}

load() {
  local dir=$1 ref tag
  if [ ! -f "$dir/references" ]; then
    echo "base-images: $dir holds no references file, so the base image job handed nothing over" >&2
    exit 1
  fi
  if [ ! -s "$dir/references" ]; then
    return 0
  fi
  docker load --input "$dir/images.tar" >&2
  while read -r ref tag; do
    printf '%s\n' --build-context "${ref}=docker-image://${tag}"
  done < "$dir/references"
}

case "${1:-}" in
  pull)
    [ "$#" -eq 3 ] || usage
    pull "$2" "$3"
    ;;
  load)
    [ "$#" -eq 2 ] || usage
    load "$2"
    ;;
  *) usage ;;
esac
