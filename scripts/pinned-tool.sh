#!/usr/bin/env bash
# pinned-tool.sh — fetch a scanner's linux x64 release once into the user
# cache, and check the binary's sha256 on every use.
#
# Usage: source this file, then
#   bin=$(pinned_tool <name> <version> <tar.gz url> <member> <sha256>)
# prints the path of a binary whose sha256 matched.
#
# Why this exists. scan-secrets.sh and lint-scripts.sh each run a scanner at
# one version, refused unless its checksum matches, and the fetch and the check
# are the same for both.

pinned_tool() {
  local name="$1" version="$2" url="$3" member="$4" sha256="$5"
  local dir="${XDG_CACHE_HOME:-$HOME/.cache}/block-kit/$name-$version"
  local bin="$dir/$name"
  if [ "$(uname -sm)" != "Linux x86_64" ]; then
    echo "pinned-tool: $name $version is pinned for Linux x86_64, and this is $(uname -sm)" >&2
    return 1
  fi
  if sha256_matches "$bin" "$sha256"; then
    printf '%s\n' "$bin"
    return 0
  fi
  fetch_member "$url" "$member" "$bin" "$sha256" || return 1
  printf '%s\n' "$bin"
}

sha256_matches() {
  [ -f "$1" ] && printf '%s  %s\n' "$2" "$1" | sha256sum --check --status -
}

# fetch_member moves the archive member into place only once its sha256 matched, so the cache never holds an unchecked binary.
fetch_member() {
  local url="$1" member="$2" bin="$3" sha256="$4" dir work
  dir=$(dirname "$bin")
  mkdir -p "$dir" || return 1
  work=$(mktemp -d "$dir/fetch.XXXXXX") || return 1
  if ! curl -fsSL "$url" -o "$work/archive.tar.gz" || ! tar -xzf "$work/archive.tar.gz" -C "$work" "$member"; then
    echo "pinned-tool: could not fetch $member from $url" >&2
    rm -rf "$work"
    return 1
  fi
  if ! sha256_matches "$work/$member" "$sha256"; then
    echo "pinned-tool: $member from $url does not match sha256 $sha256, so it is not run" >&2
    rm -rf "$work"
    return 1
  fi
  chmod +x "$work/$member" && mv -f "$work/$member" "$bin"
  local moved=$?
  rm -rf "$work"
  return "$moved"
}
