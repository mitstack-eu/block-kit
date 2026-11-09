#!/usr/bin/env bash
# copy-tool.sh copies one file out of a container image named by digest, and checks the file's sha256.
#
# Usage: copy-tool.sh <registry>/<repository>[:<tag>]@sha256:<digest> <path in the image> <sha256> <destination>
#
# Where Docker runs, it copies the file out of a container it creates and removes. Elsewhere it reads the
# image's manifest, the linux/amd64 entry of an index, its config and the layer holding the file over the
# registry's HTTP API, with an anonymous pull token once the registry asks for one, and checks each digest.
# COPY_TOOL_REGISTRY_URL, such as a test's plain-HTTP fake registry, takes every read in place of the
# image's registry. A failure names the image and the read, and the destination is written only once the
# file matches.
#
# Why this exists. A tool fetched from a release page while a machine runs fails whenever that page does,
# while an image named by digest fixes its bytes and comes from a registry.
set -euo pipefail

usage() {
  echo "Usage: copy-tool.sh <registry>/<repository>[:<tag>]@sha256:<digest> <path in the image> <sha256> <destination>" >&2
  exit 2
}

[ "$#" -eq 4 ] || usage
image=$1 path=$2 pin=$3 dest=$4
[[ $path == /* && $pin =~ ^[0-9a-f]{64}$ && $image =~ ^([^/]*[.:][^/]*)/([^@]+)@(sha256:[0-9a-f]{64})$ ]] || usage
host=${BASH_REMATCH[1]} repo=${BASH_REMATCH[2]%:*} digest=${BASH_REMATCH[3]}
[ "$host" != docker.io ] || host=registry-1.docker.io
base=${COPY_TOOL_REGISTRY_URL:-https://$host} proto='=https' token=
[ -z "${COPY_TOOL_REGISTRY_URL:-}" ] || proto='=http,https'
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Each mode reads one JSON document: a token answer, an index or manifest, or a manifest and its config,
# whose layers it lists with those whose COPY or ADD names the path or a directory above it first.
readonly JSON='
import json, sys
mode, doc = sys.argv[1], json.load(open(sys.argv[2]))
if mode == "token":
    print(doc.get("token") or doc.get("access_token", ""))
elif mode == "amd64":
    found = [m["digest"] for m in doc.get("manifests", []) if m.get("platform", {}).get("os") == "linux" and m["platform"].get("architecture") == "amd64"]
    print((found or ["none"])[0] if "manifests" in doc else "")
elif mode == "config":
    print(doc["config"]["digest"])
else:
    steps = [h.get("created_by", "") for h in json.load(open(sys.argv[3])).get("history", []) if not h.get("empty_layer")]
    layers = [l["digest"] for l in doc["layers"]]
    steps = steps if len(steps) == len(layers) else [""] * len(layers)
    def names(step):
        words = step.replace("# buildkit", "").split()
        return bool({"COPY", "ADD"} & set(words)) and (sys.argv[4] + "/").startswith(words[-1].rstrip("/") + "/")
    first = [d for d, s in zip(layers, steps) if names(s)][::-1]
    print("\n".join(first + [d for d in layers[::-1] if d not in first]))
'

fail() {
  echo "copy-tool: $image: $1: $2" >&2
  exit 1
}

json() { python3 -I -c "$JSON" "$@"; }

matches() {
  local sum
  sum=sha256:$(sha256sum "$1" | cut -d' ' -f1)
  [ "$sum" = "$2" ] || fail "$3" "has digest $sum, not $2"
}

fetch() {
  curl -sS -L --proto "$proto" --retry 3 -o "$2" -D "$work/headers" -w '%{http_code}' ${token:+-H "Authorization: Bearer $token"} \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' "$1"
}

pull_token() {
  local challenge realm service status
  challenge=$(tr -d '\r' < "$work/headers" | grep -i '^www-authenticate: *bearer' | tail -n 1) || fail "the pull token" "the registry offers no Bearer token"
  realm=$(sed -n 's/.*[Rr]ealm="\([^"]*\)".*/\1/p' <<< "$challenge")
  service=$(sed -n 's/.*[Ss]ervice="\([^"]*\)".*/\1/p' <<< "$challenge")
  status=$(curl -sS -L --proto "$proto" --retry 3 -G -o "$work/token" -w '%{http_code}' --data-urlencode "service=$service" \
    --data-urlencode "scope=repository:$repo:pull" "$realm") || fail "the pull token from $realm" "the request failed"
  [ "$status" = 200 ] || fail "the pull token from $realm" "HTTP $status, so the image is not anonymously pullable"
  json token "$work/token"
}

# get <read> <digest> <manifests|blobs> reads one object by digest into $work/<read's kind> and checks its digest.
get() {
  local file=$work/${1%% *} status
  status=$(fetch "$base/v2/$repo/$3/$2" "$file") || fail "$1" "the request failed"
  if [ "$status" = 401 ] && [ -z "$token" ]; then
    token=$(pull_token) || exit 1
    status=$(fetch "$base/v2/$repo/$3/$2" "$file") || fail "$1" "the request failed"
  fi
  [ "$status" = 200 ] || fail "$1" "the registry answered HTTP $status"
  matches "$file" "$2" "$1"
}

over_api() {
  local manifest=$digest amd64 config layer member
  get "manifest $manifest" "$manifest" manifests
  amd64=$(json amd64 "$work/manifest") || fail "manifest $manifest" "is not a manifest"
  [ "$amd64" != none ] || fail "manifest $manifest" "lists no linux/amd64 image"
  if [ -n "$amd64" ]; then
    manifest=$amd64
    get "manifest $manifest" "$manifest" manifests
  fi
  config=$(json config "$work/manifest") || fail "manifest $manifest" "names no config"
  get "config $config" "$config" blobs
  json layers "$work/manifest" "$work/config" "$path" > "$work/order" || fail "manifest $manifest" "lists no layers"
  mkdir "$work/root"
  while read -r layer; do
    echo "copy-tool: reading layer $layer of $image" >&2
    get "layer $layer" "$layer" blobs
    tar -tf "$work/layer" > "$work/members" || fail "layer $layer" "is not a tar archive"
    member=$(grep -m 1 -x -F -e "${path#/}" -e "./${path#/}" -e "$path" "$work/members") || continue
    tar -xf "$work/layer" -C "$work/root" "$member" || fail "layer $layer" "could not be unpacked"
    mv "$work/root/${path#/}" "$work/file"
    return
  done < "$work/order"
  fail "the layers of manifest $manifest" "hold no $path"
}

via_docker() {
  local id
  id=$(docker create --platform linux/amd64 "$image" /copy-tool 2> "$work/err") || fail "docker create" "$(cat "$work/err")"
  if ! docker cp "$id:$path" "$work/file" 2> "$work/err"; then
    docker rm "$id" > /dev/null 2>&1 || true
    fail "docker cp of $path" "$(cat "$work/err")"
  fi
  docker rm "$id" > /dev/null 2> "$work/err" || fail "docker rm $id" "$(cat "$work/err")"
}

if [ -z "${COPY_TOOL_REGISTRY_URL:-}" ] && docker info > /dev/null 2>&1; then via_docker; else over_api; fi
matches "$work/file" "sha256:$pin" "file $path"
mkdir -p "$(dirname "$dest")"
cp "$work/file" "$dest.copy-tool"
mv -f "$dest.copy-tool" "$dest"
echo "copy-tool: copied $path of $image to $dest"
