#!/usr/bin/env bash
# scan-image.sh — scan a local Docker image with Trivy 0.76.0, checked by
# sha256, and refuse every finding that has a fixed version, except the
# GO-2026-5932 findings go-2026-5932-exception.sh proves.
#
# Usage: scripts/scan-image.sh <image>
#
#   exit 0  Trivy found nothing with a fixed version, or only findings the
#           exception accepts
#   exit 1  a finding is refused, naming it, or Trivy or Docker failed
#
# Why this exists. publish-block.yml's publish job runs it on the image it
# built before any push, and a pull-request job can run the same scan.
set -euo pipefail

readonly TRIVY_VERSION=0.76.0
readonly TRIVY_SHA256=1f889576636db0623539cb339df7d06c40ebf39e7919489eafa1160e29808f19
readonly TRIVY_URL="https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-64bit.tar.gz"
readonly PROVIDERS=/var/cache/tofu-plugins

# shellcheck source=scripts/pinned-tool.sh
. "$(dirname "${BASH_SOURCE[0]}")/pinned-tool.sh"

main() {
  local image="${1:?usage: scan-image.sh <image>}" here trivy work container status=0
  here=$(dirname "${BASH_SOURCE[0]}")
  trivy=$(pinned_tool trivy "$TRIVY_VERSION" "$TRIVY_URL" trivy "$TRIVY_SHA256")
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  mkdir -p "$work/empty" "$work/root$(dirname "$PROVIDERS")"
  # An empty directory and no TRIVY_ variable leave no file or setting that
  # could silence a finding. The databases come from the first mirror that
  # answers.
  unset "${!TRIVY_@}"
  (cd "$work/empty" && "$trivy" image --image-src docker --exit-code 1 --ignore-unfixed --no-progress \
    --db-repository mirror.gcr.io/aquasec/trivy-db:2,public.ecr.aws/aquasecurity/trivy-db:2,ghcr.io/aquasecurity/trivy-db:2 \
    --java-db-repository mirror.gcr.io/aquasec/trivy-java-db:1,public.ecr.aws/aquasecurity/trivy-java-db:1,ghcr.io/aquasecurity/trivy-java-db:1 \
    --format json --output "$work/report.json" "$image") || status=$?
  if [ -s "$work/report.json" ]; then
    (cd "$work/empty" && "$trivy" convert --format table "$work/report.json")
  fi
  case "$status" in
    0)
      echo "scan-image: Trivy found nothing with a fixed version in $image"
      return 0
      ;;
    1) ;;
    *)
      echo "scan-image: Trivy exited $status scanning $image, so the image is refused" >&2
      return 1
      ;;
  esac
  # The exception reads the provider binaries themselves, so they leave the
  # image; an image with no provider directory leaves the exception nothing to
  # accept.
  container=$(docker create "$image" scan-image)
  docker cp "$container:$PROVIDERS" "$work/root$PROVIDERS" ||
    echo "scan-image: $image holds no $PROVIDERS, so no provider binary is copied"
  docker rm "$container" >/dev/null
  bash "$here/go-2026-5932-exception.sh" "$work/report.json" "$work/root"
}

main "$@"
