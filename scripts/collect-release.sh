#!/usr/bin/env bash
# collect-release.sh — gathers what a release signs into one directory, for publish-block.yml's publish job.
#
# Usage: scripts/collect-release.sh <image> <dir> <publisher-workflow> <file> [<file> ...]
#
# The publish job signs and pushes a release without checking out the block's
# tree or running its code, so the conformance job that built and judged the
# block hands it these: each <file> under its own name, the block's
# .github/workflows/<publisher-workflow> where it holds one, and <image>
# saved as image.tar. <dir> is emptied first, so it holds nothing else.
set -euo pipefail

if [ "$#" -lt 4 ]; then
  echo "usage: collect-release.sh <image> <dir> <publisher-workflow> <file> [<file> ...]" >&2
  exit 2
fi
image=$1 dir=$2 publisher=".github/workflows/$3"
shift 3

rm -rf "$dir"
mkdir -p "$dir"
for file in "$@"; do
  if [ ! -f "$file" ]; then
    echo "collect-release: $file is missing, so the release cannot be signed" >&2
    exit 1
  fi
  cp "$file" "$dir/$(basename "$file")"
done
# release-tag.sh starts the publisher only where it finds this file.
if [ -f "$publisher" ]; then
  mkdir -p "$dir/$(dirname "$publisher")"
  cp "$publisher" "$dir/$publisher"
fi
docker save --output "$dir/image.tar" "$image"
