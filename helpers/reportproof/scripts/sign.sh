#!/usr/bin/env bash
# Prints the key set or the Mitstack-Report-Proof of a host that holds its own Ed25519 key, byte for byte what KeySet
# and Sign make for that key, so a host with openssl and no Go signs its reports as a Go node does.
# Usage: sign.sh keyset KEY_FILE KID
#        sign.sh proof KEY_FILE KID PLATFORM_ORIGIN BODY_FILE NOW
# KEY_FILE is a PEM key from openssl genpkey -algorithm ed25519, and NOW is the Unix time the proof is minted at.
set -euo pipefail

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

mode=${1:-} key=${2:-} kid=${3:-}
[[ $kid =~ ^[A-Za-z0-9._-]+$ ]] || { echo "sign: kid $kid is not letters, digits, dots, underscores and hyphens" >&2; exit 2; }
# An Ed25519 public key's DER is this fixed prefix and the key's 32 bytes, so another key type cannot pass for one.
der=$(openssl pkey -in "$key" -pubout -outform DER | od -An -v -tx1 | tr -d ' \n')
[[ $der =~ ^302a300506032b6570032100[0-9a-f]{64}$ ]] || { echo "sign: $key holds no Ed25519 key" >&2; exit 2; }

case $mode in
keyset)
  [[ $# -eq 3 ]] || { echo "sign: keyset takes KEY_FILE KID" >&2; exit 2; }
  x=$(openssl pkey -in "$key" -pubout -outform DER | tail -c 32 | b64url)
  printf '{"keys":[{"alg":"EdDSA","crv":"Ed25519","kid":"%s","kty":"OKP","use":"sig","x":"%s"}]}' "$kid" "$x"
  ;;
proof)
  [[ $# -eq 6 ]] || { echo "sign: proof takes KEY_FILE KID PLATFORM_ORIGIN BODY_FILE NOW" >&2; exit 2; }
  origin=$4 body=$5 now=$6
  [[ $origin =~ ^https://[A-Za-z0-9.:-]+$ ]] || { echo "sign: origin $origin is not an https origin" >&2; exit 2; }
  [[ $now =~ ^(0|[1-9][0-9]{0,17})$ ]] || { echo "sign: time $now is not a whole number of seconds as Sign takes it" >&2; exit 2; }
  digest=$(sha256sum "$body" | cut -d' ' -f1)
  header=$(printf '{"alg":"EdDSA","kid":"%s","typ":"JWT"}' "$kid" | b64url)
  claims=$(printf '{"aud":["%s","report-sha256:%s"],"exp":%d,"iat":%d,"nbf":%d}' "$origin" "$digest" $((now + 600)) "$now" "$now" | b64url)
  input=$(mktemp)
  trap 'rm -f "$input"' EXIT
  printf '%s.%s' "$header" "$claims" >"$input"
  signature=$(openssl pkeyutl -sign -inkey "$key" -rawin -in "$input" | b64url)
  printf '%s.%s.%s' "$header" "$claims" "$signature"
  ;;
*)
  echo "sign: mode $mode is neither keyset nor proof" >&2
  exit 2
  ;;
esac
