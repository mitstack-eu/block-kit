// Package ghapi is the GitHub REST API client every operator that talks to
// GitHub needs — extracted once from six near-duplicate implementations
// (MIT-1565; the reference is examples/web-app-aws/app-scaffold/urlguard.go's
// host guard plus examples/web-app-aws/managed-ci/repos.go's repo
// operations).
//
// # The host is the operator's decision, never the caller's (MIT-1325)
//
// Base resolves a request-supplied endpoint against a fixed allowlist
// (api.github.com plus any operator-configured extraHosts, e.g.
// MITSTACK_GITHUB_API_HOSTS for GitHub Enterprise) via safe.TrustedBase, and
// on top of that refuses any resolved host that is private, loopback,
// link-local, unspecified, a bare IP literal, "localhost", or contains
// "metadata" — EVEN when that exact host is one of the operator's own
// extraHosts. An operator misconfiguration (a typo, a compromised env var)
// must never be able to point a real installation token at an internal or
// cloud-metadata address.
//
// Base is unconditionally https, with NO loopback/http carve-out anywhere.
// The reference implementation special-cases http://127.0.0.1 inline inside
// the security-critical host guard, for its own httptest fixtures — a silent
// branch inside the function callers must trust. This package has no such
// branch: production converts the endpoint ONCE, visibly, via
// Base(endpoint, extraHosts...).String(), and tests hand httptest URLs
// straight to NewClient (see client.go). Do not re-add an http carve-out
// here to make a test easier — route the test around Client instead, exactly
// as this package's own tests do.
package ghapi

import (
	"fmt"
	"net"
	"strings"

	"github.com/mitstack-eu/operator-kit/safe"
)

// APIGitHubHost is the standard github.com REST API host, always allowed.
const APIGitHubHost = "api.github.com"

// Base resolves endpoint against the allowlist {APIGitHubHost} ∪ extraHosts
// and returns a safe.Base built from the ALLOWLIST ENTRY — never from
// endpoint itself (see safe.TrustedBase). It additionally refuses any
// resolved host that is private/loopback/link-local/unspecified, a bare IP
// literal, "localhost", or contains "metadata", even if that host came from
// extraHosts.
func Base(endpoint string, extraHosts ...string) (safe.Base, error) {
	allow := safe.NewAllowlist(append([]string{APIGitHubHost}, extraHosts...)...)
	b, err := safe.TrustedBase(endpoint, allow)
	if err != nil {
		return safe.Base{}, err
	}
	host := strings.TrimPrefix(b.String(), "https://")
	if !isPublicHost(host) {
		return safe.Base{}, fmt.Errorf("ghapi: host %q is not an acceptable GitHub API host — private, loopback, link-local, metadata and bare-IP hosts are refused even when operator-allowlisted", host)
	}
	return b, nil
}

// isPublicHost rejects every bare IP literal — private, loopback, link-local,
// unspecified and public alike — plus the well-known localhost and metadata
// hostnames. Only a real hostname is an acceptable GitHub Enterprise host
// shape. Brackets and a trailing port are stripped before net.ParseIP so a
// literal like "[::1]" is caught (mirrors the reference's isPublicGitHubHost
// in urlguard.go).
//
// It does NOT resolve DNS, and that is deliberate rather than an oversight: a
// resolution here would be decorative, because the address resolved at connect
// time can differ from the one resolved at validation time. The value being
// checked is the operator's own configuration, not anything a caller supplied,
// so a hostname an operator allowlists that happens to resolve internally is
// accepted. Egress restrictions, not this function, bound where it may land.
func isPublicHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	if hostOnly, _, err := net.SplitHostPort(h); err == nil {
		h = hostOnly
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")

	if h == "localhost" || strings.Contains(h, "metadata") {
		return false
	}
	if net.ParseIP(h) != nil {
		// Any IP literal is refused, public or not — conservative: only
		// hostnames are an acceptable GitHub Enterprise host shape.
		return false
	}
	return true
}
