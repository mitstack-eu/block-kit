// Package ghapi is the GitHub REST API client every operator that talks to
// GitHub needs.
//
// # The host is the operator's decision, never the caller's
//
// An operator resolves a request-supplied endpoint with BaseFromEnv, which
// checks it against api.github.com plus the hosts the operator lists in
// MITSTACK_GITHUB_API_HOSTS (for GitHub Enterprise). BaseFromEnv delegates to
// Base, which resolves via safe.TrustedBase and on top of that refuses any
// resolved host that is private, loopback, link-local, unspecified, a bare IP
// literal, "localhost", or contains "metadata" — EVEN when that exact host is
// one the operator listed. An operator misconfiguration (a typo, a
// compromised env var) must never be able to point a real installation token
// at an internal or cloud-metadata address.
//
// Resolution is unconditionally https, with NO loopback/http carve-out
// anywhere: a silent branch inside the function callers must trust is exactly
// what this package refuses to carry. Production resolves the endpoint ONCE,
// visibly, with BaseFromEnv and hands the resulting base's String() to
// NewClient, while tests hand httptest URLs straight to NewClient (see
// client.go). Do not add an http carve-out here to make a test easier — route
// the test around Client instead, exactly as this package's own tests do.
package ghapi

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/mitstack-eu/operator-kit/safe"
)

// APIGitHubHost is the standard github.com REST API host, always allowed.
const APIGitHubHost = "api.github.com"

// HostsEnv names the comma-separated list of extra GitHub API hosts an operator allows.
const HostsEnv = "MITSTACK_GITHUB_API_HOSTS"

// BaseFromEnv resolves endpoint with Base against the extra hosts listed in HostsEnv.
func BaseFromEnv(endpoint string) (safe.Base, error) {
	return Base(endpoint, strings.Split(os.Getenv(HostsEnv), ",")...)
}

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
// literal like "[::1]" is caught.
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
