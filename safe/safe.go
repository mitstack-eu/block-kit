// Package safe holds the three security invariants every operator must honour
// and each of which has already been violated AFTER being fixed, because each
// was re-implemented per operator (MIT-1472).
//
//  1. The OPERATOR decides which API host it talks to, never the caller
//     (MIT-1325). A webhook's `github` endpoint may be COMPARED, never
//     propagated into an outbound URL.
//  2. CR/LF is stripped from request-derived text BEFORE control characters,
//     with explicit ReplaceAll calls (MIT-1326). The order is load-bearing.
//  3. Scrubbing secrets is NOT sanitising newlines. The two compose; they are
//     routinely confused and substituted for one another.
//
// The host rule is enforced by TYPE rather than by convention. TrustedBase
// returns a Base — a distinct type the URL builders require — so a
// request-supplied host cannot be concatenated into a URL without a compile
// error. That is a stronger guarantee than the script that hunts for the
// pattern, and the reason it is needed is instructive: `managed-ci`'s
// SetVariable kept a LIVE SSRF via a raw `apiBase +` concatenation after every
// sibling call site had already been fixed.
package safe

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Base is a host the operator has decided it is willing to talk to. It is
// deliberately a distinct type: the URL builders below accept only a Base, so a
// string that came in on a request cannot become an outbound URL without
// someone explicitly, visibly converting it.
type Base struct {
	scheme string
	host   string
}

// String renders the base as a URL prefix, e.g. "https://api.github.com".
func (b Base) String() string {
	if b.host == "" {
		return ""
	}
	return b.scheme + "://" + b.host
}

// IsZero reports whether the base was never resolved.
func (b Base) IsZero() bool { return b.host == "" }

// URL joins a path onto the base. The path is escaped, and the host half comes
// from the allowlist entry — never from the caller-supplied endpoint that
// TrustedBase merely compared.
func (b Base) URL(path string) string {
	if b.IsZero() {
		return ""
	}
	return b.String() + "/" + strings.TrimLeft(path, "/")
}

// Allowlist is the set of hosts an operator will talk to. Every entry must come
// from a compile-time constant or from operator configuration (an env var set
// where the operator is deployed) — NEVER from a request.
type Allowlist struct {
	hosts []string
}

// NewAllowlist builds an allowlist from operator-owned values. Entries are
// lower-cased and de-duplicated; empty ones are dropped.
func NewAllowlist(hosts ...string) Allowlist {
	seen := map[string]bool{}
	var out []string
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return Allowlist{hosts: out}
}

// Hosts returns the allowlisted hosts, for diagnostics.
func (a Allowlist) Hosts() []string { return append([]string(nil), a.hosts...) }

// TrustedBase resolves a request-supplied endpoint against the operator's own
// allowlist and returns a Base built from the ALLOWLIST ENTRY.
//
// The endpoint is only ever COMPARED. Nothing derived from it reaches the
// returned Base, which is what ends the taint chain — and is exactly what the
// first fix got wrong: it validated the host and then returned
// `"https://" + u.Host`, which is both a live SSRF and the CodeQL
// go/request-forgery carrier. Validating a value and then using it is not the
// same as not using it.
func TrustedBase(endpoint string, allow Allowlist) (Base, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(endpoint), "/"))
	if err != nil || u.Host == "" {
		return Base{}, fmt.Errorf("safe: invalid api base %q", endpoint)
	}
	if u.Scheme != "https" {
		return Base{}, fmt.Errorf("safe: api base %q must be https", endpoint)
	}

	host := strings.ToLower(u.Host)
	for _, allowed := range allow.hosts {
		if host == allowed {
			// `allowed` is operator-owned. The Base is built from it, not from
			// `host`, so no request-derived string survives this function.
			return Base{scheme: "https", host: allowed}, nil
		}
	}
	return Base{}, fmt.Errorf("safe: api base %q is not in this operator's allowlist %v — the operator decides the host, not the caller (MIT-1325)",
		endpoint, allow.hosts)
}

// --- Log sanitisation (MIT-1326) --------------------------------------------

var controlCharPattern = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// LogValue makes a request-derived value safe to write to a log.
//
// The ORDER is load-bearing and is the entire reason this is a function rather
// than a one-liner at each call site. CR and LF are removed by explicit
// ReplaceAll calls FIRST; residual control characters are stripped second. A
// single strings.Map pass removes exactly the same characters — and is not
// recognised as a sanitisation barrier, so the taint analysis still reported
// request data reaching the logger (go/log-injection). Do not "simplify" this
// into one pass.
//
// This is NOT secret scrubbing. See Scrub.
func LogValue(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return controlCharPattern.ReplaceAllString(s, "")
}

// Scrub replaces every occurrence of each secret with a redaction marker.
//
// It is ORTHOGONAL to LogValue and the two are composed, never substituted:
// Scrub removes SECRETS and does not remove newlines; LogValue removes newlines
// and does not know what a secret is. Confusing them leaves one of the two
// holes open, which is what MIT-1326 found.
func Scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) < 4 {
			// Too short to redact without mangling ordinary text.
			continue
		}
		s = strings.ReplaceAll(s, sec, "[REDACTED]")
	}
	return s
}

// LogSafe is the composition every log call site wants: scrub the secrets, then
// sanitise the result for the logger. Provided so that "which one do I need?"
// has one answer — both, in this order.
func LogSafe(s string, secrets ...string) string {
	return LogValue(Scrub(s, secrets...))
}
