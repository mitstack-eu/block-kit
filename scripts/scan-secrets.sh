#!/usr/bin/env bash
# scan-secrets.sh — scan every commit a repository's HEAD reaches for secrets,
# with gitleaks 8.21.2 checked by sha256.
#
# Usage: scripts/scan-secrets.sh <repository>
#
#   exit 0  no commit holds a secret
#   exit 1  a commit holds one, and gitleaks names the commit, file and rule
#           with the secret redacted, or the pinned gitleaks could not be run
#
# Why this exists. publish-block.yml's secrets job runs it on every block, and
# block-kit's tests run it on throwaway repositories, so the job and its proof
# run one scan.
set -euo pipefail

readonly GITLEAKS_VERSION=8.21.2
readonly GITLEAKS_SHA256=50b742abd7daad8bbddb6301f3017efb680632d9a5b3b4d8f137b3aac250e359
readonly GITLEAKS_URL="https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_linux_x64.tar.gz"

# shellcheck source=scripts/pinned-tool.sh
. "$(dirname "${BASH_SOURCE[0]}")/pinned-tool.sh"

main() {
  local repo="${1:?usage: scan-secrets.sh <repository>}" gitleaks
  gitleaks=$(pinned_tool gitleaks "$GITLEAKS_VERSION" "$GITLEAKS_URL" gitleaks "$GITLEAKS_SHA256")
  # gitleaks reads .gitleaksignore from its working directory, so it runs
  # from the repository it scans.
  cd "$repo"
  "$gitleaks" git --no-banner --redact --verbose --log-opts=HEAD .
}

main "$@"
