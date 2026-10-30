#!/usr/bin/env bash
# Prints the Mitstack-Report-Proof of a node report: a token the Kubernetes API server signs for SERVICE_ACCOUNT, naming
# the platform's origin and the SHA-256 of the exact body, so the node itself holds no private key.
# Usage: token-request.sh BODY_FILE REPORT_URL NAMESPACE SERVICE_ACCOUNT
# It exists so every Kubernetes cluster block signs its reports one way; SERVICE_ACCOUNT needs create on its own
# serviceaccounts/token and nothing more.
set -euo pipefail

body=$1 url=$2 namespace=$3 account=$4
[[ $url == https://* ]] || { echo "token-request: $url is not https" >&2; exit 2; }
origin=${url#https://}
digest=$(sha256sum "$body" | cut -d' ' -f1)
exec kubectl create token "$account" -n "$namespace" --audience "https://${origin%%/*}" --audience "report-sha256:$digest" --duration 10m
