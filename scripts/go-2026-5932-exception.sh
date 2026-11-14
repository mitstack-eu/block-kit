#!/usr/bin/env bash
# go-2026-5932-exception.sh — judge a Trivy image report, accepting the
# advisory GO-2026-5932 only in an OpenTofu provider binary whose Go function
# table names golang.org/x/crypto code but none of golang.org/x/crypto/openpgp,
# and only while that provider's newest release still requires an affected
# golang.org/x/crypto. A dated exception also accepts the findings it lists,
# only in the one provider binary its sha256 names and only through its last
# day. Every other finding is refused. govulncheck 1.9.0, checked by its
# module sum, also scans each binary that holds GO-2026-5932. Its result is
# printed but does not decide, and when it cannot run the script says so.
#
# Usage: scripts/go-2026-5932-exception.sh <trivy-report.json> <image-root>
#
#   <image-root> holds the image's files at their paths in the image, and
#   needs only the provider binaries the report names.
#
#   exit 0  every finding is GO-2026-5932 in a provider binary proven so, or
#           one the dated exception lists in its binary through its last day
#   exit 1  a finding is refused, naming it and why, a read failed, or the
#           report holds no finding at all
#
# PROVIDER_REGISTRY_URL replaces https://<registry host>, PROVIDER_SOURCE_URL
# replaces https://raw.githubusercontent.com and GOVULNCHECK_DB_URL replaces
# https://vuln.go.dev, so a test can answer every read.
#
# Why this exists. GO-2026-5932 marks the whole openpgp package unsafe, and its
# only fix is an untagged golang.org/x/crypto commit, so Trivy reports it in
# every provider built on a tagged release, whether or not the provider links
# openpgp. govulncheck cannot tell which do, since provider releases are
# stripped of the symbols it reads, while the function table every Go binary
# keeps for its stack traces names each function compiled in. The dated
# exception covers findings that no release of the provider fixes yet.
set -euo pipefail
export LC_ALL=C

readonly ADVISORY=GO-2026-5932
readonly FIXED=v0.58.1-0.20261009181718-2f1f834330b2
readonly CRYPTO=golang.org/x/crypto
readonly OPENPGP=golang.org/x/crypto/openpgp
readonly PROVIDERS=var/cache/tofu-plugins
readonly PROVIDER_PATH="^$PROVIDERS/([^/]+)/([^/]+)/([^/]+)/([0-9]+\.[0-9]+\.[0-9]+)/[^/]+/(terraform-provider-[^/]+)$"
readonly SEMVER='^v([0-9]+)\.([0-9]+)\.([0-9]+)(-([0-9A-Za-z.-]+))?(\+[0-9A-Za-z.-]+)?$'
readonly SEP=$'\x1f'
readonly GOVULNCHECK=golang.org/x/vuln/cmd/govulncheck@v1.9.0
readonly GOVULNCHECK_SUM=h1:12VaqnI9kJfqwRoPptxoqsUhQtdfrSbdksT7X/KY/sc=

# The Scaleway provider's newest release, 2.92.1, is built with Go 1.27.3 and
# golang.org/x/net v0.61.0, and only Go 1.27.4 and x/net v0.62.0 fix these. A
# sha256 names one binary, so a rebuild or any other file at the same path is
# refused.
readonly DATED_BINARY=$PROVIDERS/registry.opentofu.org/scaleway/scaleway/2.92.1/linux_amd64/terraform-provider-scaleway_v2.92.1
readonly DATED_SHA256=d5868a2c11a9241b8c55c44ffd8b20c60b44ec6398f224e3f4d14c2bf41614b7
readonly DATED_LAST_DAY=2026-12-14
readonly DATED_FINDINGS=("CVE-2026-78661 golang.org/x/net" "CVE-2026-97045 golang.org/x/net" "CVE-2026-97054 stdlib")

work=""
funcnames=""
govulncheck=""
govulncheck_missing=""

# findings prints one row per finding in the report, its fields split by SEP,
# and fails on a report that is not Trivy's schema 2.
findings() {
  jq -r --arg sep "$SEP" '
    if .SchemaVersion != 2 then error("not a Trivy schema 2 report") else . end
    | .Results[]? | .Target as $t | (.Type // "-") as $type
    | ( ((.Vulnerabilities // [])[] | ["vuln", $t, $type, .VulnerabilityID, .PkgName, .InstalledVersion, (.FixedVersion // "-")]),
        ((.Secrets // [])[] | ["secret", $t, $type, .RuleID, "-", "-", "-"]),
        ((.Misconfigurations // [])[] | ["misconfiguration", $t, $type, .ID, "-", "-", "-"]),
        ((.Licenses // [])[] | ["license", $t, $type, .Name, .PkgName, "-", "-"]) )
    | map(tostring) | join($sep)' "$1"
}

# fetch writes the body at $1 to stdout, or the reason it could not be read.
fetch() {
  local reason
  if ! reason=$(curl -fsSL --max-time 60 "$1" 2>&1 >"$work/body"); then
    echo "could not read $1 (${reason#curl: })"
    return 1
  fi
  cat "$work/body"
}

# version_at_least succeeds when Go module version $1 orders at or above $2
# under semver, so a pseudo-version orders below the release it precedes.
version_at_least() {
  local -a a b x y
  [[ $1 =~ $SEMVER ]] && a=("${BASH_REMATCH[@]:1:3}" "${BASH_REMATCH[5]}")
  [[ $2 =~ $SEMVER ]] && b=("${BASH_REMATCH[@]:1:3}" "${BASH_REMATCH[5]}")
  local i
  for i in 0 1 2; do
    if ((10#${a[i]} != 10#${b[i]})); then
      ((10#${a[i]} > 10#${b[i]}))
      return
    fi
  done
  [ -z "${a[3]}" ] && return 0
  [ -z "${b[3]}" ] && return 1
  IFS=. read -r -a x <<<"${a[3]}"
  IFS=. read -r -a y <<<"${b[3]}"
  for ((i = 0; i < ${#x[@]} && i < ${#y[@]}; i++)); do
    [ "${x[i]}" = "${y[i]}" ] && continue
    if [[ ${x[i]} =~ ^[0-9]+$ && ${y[i]} =~ ^[0-9]+$ ]]; then
      ((10#${x[i]} > 10#${y[i]}))
      return
    fi
    [[ ${x[i]} =~ ^[0-9]+$ ]] && return 1
    [[ ${y[i]} =~ ^[0-9]+$ ]] && return 0
    [[ ${x[i]} > ${y[i]} ]]
    return
  done
  ((${#x[@]} >= ${#y[@]}))
}

# install_tools builds gofuncnames from this kit, failing without it, and
# installs govulncheck, which runs only when its module sum matches its pin.
install_tools() {
  local bin="${XDG_CACHE_HOME:-$HOME/.cache}/block-kit/govulncheck-v1.9.0" sum
  GOFLAGS='' GOWORK=off go build -C "$(dirname "${BASH_SOURCE[0]}")/.." -o "$work/gofuncnames" ./cmd/gofuncnames || return 1
  funcnames="$work/gofuncnames"
  if ! GOBIN="$bin" GOFLAGS='' GOWORK=off go install "$GOVULNCHECK" 2>"$work/install.err"; then
    govulncheck_missing="go install $GOVULNCHECK failed: $(tail -n 3 "$work/install.err" | tr '\n' ' ')"
    return 0
  fi
  sum=$(go version -m "$bin/govulncheck" | awk '$1 == "mod" {print $4}')
  if [ "$sum" != "$GOVULNCHECK_SUM" ]; then
    govulncheck_missing="$bin/govulncheck has module sum '$sum', not $GOVULNCHECK_SUM"
    return 0
  fi
  govulncheck="$bin/govulncheck"
}

# govulncheck_result prints govulncheck's binary-mode result for binary $1
# under a heading saying why it does not decide, or why it did not run.
govulncheck_result() {
  local bin="$1" db="${GOVULNCHECK_DB_URL:-https://vuln.go.dev}" precision
  if [ -z "$govulncheck" ]; then
    echo "  govulncheck did not run, so the function table alone decides: $govulncheck_missing"
    return
  fi
  if ! "$govulncheck" -mode binary -format json -db "$db" "$bin" >"$work/vuln.json" 2>"$work/vuln.err"; then
    echo "  govulncheck could not scan it against $db, so the function table alone decides: $(tail -n 3 "$work/vuln.err" | tr '\n' ' ')"
    return
  fi
  if [ "$("$funcnames" -symtab "$bin")" = absent ]; then
    precision="ran at go.mod-level precision because the binary is stripped, so it cannot show which packages are present, and it does not decide"
  else
    precision="read the binary's symbols, and it does not decide"
  fi
  jq -rs --arg a "$ADVISORY" --arg p "$precision" '
    (map(.config? // empty) | first | "  govulncheck \(.scanner_version) against \(.db), modified \(.db_last_modified), \($p):"),
    ([.[] | .finding? // empty | select(.osv == $a) | .trace[0]] as $t
     | ($t | map(select(.package == null) | "\(.module)@\(.version)") | unique) as $mods
     | ($t | map(.package // empty) | unique) as $pkgs
     | if ($t | length) == 0 then "    no \($a) finding"
       else "    \($a) at module \($mods | join(", ")), and in \($pkgs | length) package(s): \($pkgs | join(", "))" end)' "$work/vuln.json"
}

# no_openpgp_code prints what the function table of binary $1 holds, failing
# unless it names x/crypto code and no openpgp code.
no_openpgp_code() {
  local bin="$1" crypto openpgp
  if ! "$funcnames" "$bin" >"$work/names" 2>"$work/names.err"; then
    echo "its Go function table could not be read: $(cat "$work/names.err")"
    return 1
  fi
  openpgp=$(grep -cF "$OPENPGP" "$work/names" || true)
  if [ "$openpgp" -gt 0 ]; then
    echo "its Go function table names $openpgp functions in $OPENPGP:"
    grep -F "$OPENPGP" "$work/names" | sed 's/^/    /'
    return 1
  fi
  crypto=$(grep -cF "$CRYPTO/" "$work/names" || true)
  if [ "$crypto" -eq 0 ]; then
    echo "its Go function table names no $CRYPTO function, so it proves nothing about $OPENPGP"
    return 1
  fi
  echo "its Go function table names $crypto $CRYPTO functions and 0 in $OPENPGP"
}

# no_fixed_release prints what provider $1/$2/$3's newest release requires,
# failing once that release requires the fix or no x/crypto at all.
no_fixed_release() {
  local host="$1" namespace="$2" type="$3" installed="$4" bin="$5"
  local provider="$1/$2/$3" list newest module repo url gomod required
  list=$(fetch "${PROVIDER_REGISTRY_URL:-https://$host}/v1/providers/$namespace/$type/versions") || {
    echo "$list, so whether $provider has shipped a release on a fixed $CRYPTO is unknown"
    return 1
  }
  newest=$(jq -r '[.versions[]?.version | select(test("^[0-9]+\\.[0-9]+\\.[0-9]+$"))] | sort_by(split(".") | map(tonumber)) | last // empty' <<<"$list" 2>/dev/null || true)
  if [ -z "$newest" ]; then
    echo "the registry lists no release of $provider, so whether it has shipped one on a fixed $CRYPTO is unknown"
    return 1
  fi
  module=$(go version -m "$bin" 2>/dev/null | awk '$1 == "mod" {print $2; exit}' || true)
  repo=$(sed -E 's#/v[0-9]+$##' <<<"$module")
  if [[ ! $repo =~ ^github\.com/[^/]+/[^/]+$ ]]; then
    echo "its main module '$module' names no GitHub repository, so the go.mod of $provider $newest cannot be read"
    return 1
  fi
  url="${PROVIDER_SOURCE_URL:-https://raw.githubusercontent.com}/${repo#github.com/}/v$newest/go.mod"
  gomod=$(fetch "$url") || {
    echo "$gomod, so whether $provider $newest is built on a fixed $CRYPTO is unknown"
    return 1
  }
  if ! grep -q '^module ' <<<"$gomod"; then
    echo "$url holds no module line, so whether $provider $newest is built on a fixed $CRYPTO is unknown"
    return 1
  fi
  required=$(awk -v m="$CRYPTO" '
    $2 == "(" { inrequire = ($1 == "require"); next }
    $1 == ")" { inrequire = 0; next }
    $1 == "require" && $2 == m { print $3; exit }
    inrequire && $1 == m { print $2; exit }' <<<"$gomod")
  if [ -z "$required" ]; then
    echo "$provider $newest requires no $CRYPTO, so the exception no longer covers $installed; move the block to $provider $newest"
    return 1
  fi
  if [[ ! $required =~ $SEMVER ]]; then
    echo "$provider $newest requires $CRYPTO '$required', which is not a module version, so whether it is fixed is unknown"
    return 1
  fi
  if version_at_least "$required" "$FIXED"; then
    echo "$provider $newest requires $CRYPTO $required, at or above the fix $FIXED, so the exception no longer covers $installed; move the block to $provider $newest"
    return 1
  fi
  echo "$provider's newest release $newest still requires $CRYPTO $required, below the fix $FIXED"
}

# dated_finding succeeds when finding $1 in module $2 is one the dated
# exception lists.
dated_finding() {
  local listed
  for listed in "${DATED_FINDINGS[@]}"; do
    [ "$listed" = "$1 $2" ] && return 0
  done
  return 1
}

# judge_dated prints the verdict on dated finding $4 in module $5 of binary $2
# and fails when it is refused.
judge_dated() {
  local root="$1" target="$2" type="$3" id="$4" pkg="$5" today digest
  if [ "$type" != gobinary ] || [ "$target" != "$DATED_BINARY" ]; then
    echo "refused $id in $target: its dated exception covers only $DATED_BINARY"
    return 1
  fi
  today=$(date -u +%Y-%m-%d)
  if [[ ! $today =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
    echo "refused $id in $target: date printed '$today', not today's date, so its dated exception cannot be judged"
    return 1
  fi
  if [[ $today > $DATED_LAST_DAY ]]; then
    echo "refused $id in $target: its dated exception lasted through $DATED_LAST_DAY UTC and today is $today, so move the block to a provider release that fixes $pkg"
    return 1
  fi
  if [ ! -f "$root/$target" ]; then
    echo "refused $id in $target: the image holds no such file"
    return 1
  fi
  digest=$(sha256sum "$root/$target" | awk '{print $1}')
  if [ "$digest" != "$DATED_SHA256" ]; then
    echo "refused $id in $target: the binary's sha256 is $digest, not the release's $DATED_SHA256, so its dated exception does not cover it"
    return 1
  fi
  echo "accepted $id in $target: the binary's sha256 is the release's $DATED_SHA256, and this dated exception lasts through $DATED_LAST_DAY UTC"
}

# judge prints the verdict on one finding and fails when it is refused.
judge() {
  local root="$1" kind="$2" target="$3" type="$4" id="$5" pkg="$6" installed="$7" fixed="$8" proof release
  if [ "$kind" != vuln ]; then
    echo "refused $kind $id in $target"
    return 1
  fi
  if dated_finding "$id" "$pkg"; then
    judge_dated "$root" "$target" "$type" "$id" "$pkg"
    return
  fi
  if [ "$id" != "$ADVISORY" ] || [ "$pkg" != "$CRYPTO" ]; then
    echo "refused $id in $target: $pkg $installed has a fix in $fixed"
    return 1
  fi
  if [ "$type" != gobinary ] || [[ ! $target =~ $PROVIDER_PATH ]] ||
    [[ ${BASH_REMATCH[5]} != "terraform-provider-${BASH_REMATCH[3]}" && ${BASH_REMATCH[5]} != "terraform-provider-${BASH_REMATCH[3]}_"* ]]; then
    echo "refused $id in $target: the exception covers only an OpenTofu provider binary under /$PROVIDERS"
    return 1
  fi
  local -a at=("${BASH_REMATCH[@]:1:4}")
  if [ ! -f "$root/$target" ]; then
    echo "refused $id in $target: the image holds no such file"
    return 1
  fi
  govulncheck_result "$root/$target"
  proof=$(no_openpgp_code "$root/$target") || {
    echo "refused $id in $target: $proof"
    return 1
  }
  release=$(no_fixed_release "${at[@]}" "$root/$target") || {
    echo "refused $id in $target: $release"
    return 1
  }
  echo "accepted $id in $target: $proof, and $release"
}

main() {
  local report="${1:?usage: go-2026-5932-exception.sh <trivy-report.json> <image-root>}"
  local root="${2:?usage: go-2026-5932-exception.sh <trivy-report.json> <image-root>}"
  local rows refused=0 total=0 kind target type id pkg installed fixed
  if ! rows=$(findings "$report"); then
    echo "go-2026-5932-exception: could not read the Trivy report $report, so no finding is accepted"
    return 1
  fi
  if [ -z "$rows" ]; then
    echo "go-2026-5932-exception: the Trivy report $report holds no finding, so nothing in it explains why the scan failed"
    return 1
  fi
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  if grep -qF "$SEP$ADVISORY$SEP" <<<"$rows" && ! install_tools; then
    echo "go-2026-5932-exception: could not build gofuncnames, so no finding is accepted"
    return 1
  fi
  while IFS="$SEP" read -r -u 3 kind target type id pkg installed fixed; do
    [ -n "$kind" ] || continue
    total=$((total + 1))
    judge "$root" "$kind" "$target" "$type" "$id" "$pkg" "$installed" "$fixed" || refused=$((refused + 1))
  done 3<<<"$rows"
  if [ "$refused" -gt 0 ]; then
    echo "go-2026-5932-exception: refused $refused of $total finding(s); only $ADVISORY in a provider binary proven free of $OPENPGP code, and the dated exception's findings in $DATED_BINARY through $DATED_LAST_DAY, are accepted"
    return 1
  fi
  echo "go-2026-5932-exception: accepted all $total finding(s), each $ADVISORY in a provider binary proven free of $OPENPGP code or a dated exception's finding in $DATED_BINARY"
}

main "$@"
