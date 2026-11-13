// Package safe, an optional helper, holds the security invariants an operator must honour when it logs a request's values or calls a host a request names: the operator picks the API host, and log sanitising and secret scrubbing are kept apart.
package safe

import (
	"cmp"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const redactionMarker = "[REDACTED]"

// ShortestSecretScrubbed is the length of the shortest secret Scrub replaces, since a shorter one cannot be told from ordinary text.
const ShortestSecretScrubbed = 4

// Base is a host the operator is willing to talk to; the URL builders accept only a Base, so a request-supplied string cannot become an outbound URL without a visible conversion.
type Base struct {
	scheme string
	host   string
}

func (b Base) String() string {
	if b.host == "" {
		return ""
	}
	return b.scheme + "://" + b.host
}

func (b Base) IsZero() bool { return b.host == "" }

func (b Base) URL(path string) string {
	if b.IsZero() {
		return ""
	}
	return b.String() + "/" + strings.TrimLeft(path, "/")
}

// Allowlist is the set of hosts an operator will talk to, which must come from constants or operator configuration, never a request.
type Allowlist struct {
	hosts []string
}

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

func (a Allowlist) Hosts() []string { return append([]string(nil), a.hosts...) }

// TrustedBase returns a Base built from the allowlist entry, never from the endpoint, which is only compared (building from it is an SSRF).
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
			return Base{scheme: "https", host: allowed}, nil
		}
	}
	return Base{}, fmt.Errorf("safe: api base %q is not in this operator's allowlist %v — the operator decides the host, not the caller",
		endpoint, allow.hosts)
}

var controlCharPattern = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// LogValue makes a request-derived value safe to log; CR and LF go by explicit ReplaceAll first, since a single strings.Map pass is not a go/log-injection barrier.
func LogValue(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return controlCharPattern.ReplaceAllString(s, "")
}

// Scrub replaces every stretch of s that secrets cover with a redaction marker, whatever order they arrive in, and does not remove newlines; compose it with LogValue rather than substituting one for the other.
func Scrub(s string, secrets ...string) string {
	covered := coveredSpans(s, secrets)
	if len(covered) == 0 {
		return s
	}
	var scrubbed strings.Builder
	kept := 0
	for _, stretch := range covered {
		scrubbed.WriteString(s[kept:stretch.start])
		scrubbed.WriteString(redactionMarker)
		kept = stretch.end
	}
	scrubbed.WriteString(s[kept:])
	return scrubbed.String()
}

type span struct{ start, end int }

// coveredSpans joins occurrences that overlap, since masking one alone leaves the rest of the other readable beside the marker.
func coveredSpans(s string, secrets []string) []span {
	occurrences := occurrencesOf(s, secrets)
	slices.SortFunc(occurrences, func(a, b span) int { return cmp.Compare(a.start, b.start) })
	var covered []span
	for _, occurrence := range occurrences {
		last := len(covered) - 1
		if last < 0 || occurrence.start >= covered[last].end {
			covered = append(covered, occurrence)
			continue
		}
		covered[last].end = max(covered[last].end, occurrence.end)
	}
	return covered
}

// occurrencesOf searches on from one byte past each match, so a secret that overlaps its own next occurrence is found both times.
func occurrencesOf(s string, secrets []string) []span {
	var found []span
	for _, secret := range secrets {
		if len(secret) < ShortestSecretScrubbed {
			continue
		}
		for from := 0; ; {
			at := strings.Index(s[from:], secret)
			if at < 0 {
				break
			}
			start := from + at
			found = append(found, span{start: start, end: start + len(secret)})
			from = start + 1
		}
	}
	return found
}

func LogSafe(s string, secrets ...string) string {
	return LogValue(Scrub(s, secrets...))
}
