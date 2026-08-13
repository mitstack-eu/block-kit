// Package customdomain validates the optional, consumer-supplied vanity
// hostname (MIT-1805) that the aws-topology capability's three engines
// (OpenTofu, CloudFormation, Crossplane) all accept as a `customDomain`
// param.
//
// WHY THIS LIVES HERE, ONCE
//
// The value rides straight into the operator's Caddy vhost + ACME config on
// the consumer's own box, so it is DATA, never config (the same threat class
// as MIT-1325's GitHub host allowlist) — every engine that accepts it must
// reject anything not shaped like a bare RFC-1035 hostname BEFORE it ever
// reaches a rendered script or a cloud API call. Three engines, one
// validation rule: duplicating it per engine is exactly the drift
// `scripts/check-reconcile-scripts-sync.sh` and this package's sibling
// `tftpl` already exist to prevent for the reconcile scripts themselves.
package customdomain

import (
	"fmt"
	"strings"
)

const (
	// maxHostnameLength / maxLabelLength mirror RFC 1035 §3.1's limits: 255
	// octets total including the root's implicit trailing dot (255-1=254; the
	// conventional presentation-form limit used by browsers/ACME clients is
	// 253), 63 octets per label.
	maxHostnameLength = 253
	maxLabelLength    = 63
)

// Validate rejects anything that is not a bare RFC-1035-shaped hostname.
// Empty is the documented opt-out (the feature-off default) and is always
// valid — every other case must be a plain consumer-owned hostname: no
// scheme, path, port, wildcard, control character, or Caddyfile-directive
// shape (space/brace/CR/LF) can ever reach an operator's Caddy/ACME config.
func Validate(domain string) error {
	if domain == "" {
		return nil
	}

	for _, r := range domain {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("customDomain must not contain control characters (found %q)", r)
		}
	}
	if strings.ContainsAny(domain, " \t{}") {
		return fmt.Errorf("customDomain must not contain spaces or braces — it must be a bare hostname, not config")
	}
	if strings.Contains(domain, "://") {
		return fmt.Errorf("customDomain must not include a URL scheme — pass a bare hostname")
	}
	if strings.ContainsAny(domain, "/:") {
		return fmt.Errorf("customDomain must be a bare hostname, not a URL with a path or port")
	}
	if strings.Contains(domain, "*") {
		return fmt.Errorf("customDomain must not be a wildcard")
	}
	if len(domain) > maxHostnameLength {
		return fmt.Errorf("customDomain exceeds the maximum hostname length of %d characters", maxHostnameLength)
	}

	for _, label := range strings.Split(domain, ".") {
		if err := validateLabel(label); err != nil {
			return err
		}
	}
	return nil
}

// validateLabel validates a single dot-separated hostname label: non-empty
// (catches leading/trailing/double dots via the empty split result), at most
// 63 characters, and letters/digits/hyphens only — never starting or ending
// with a hyphen.
func validateLabel(label string) error {
	if label == "" {
		return fmt.Errorf("customDomain must not contain an empty label (leading dot, trailing dot, or double dot)")
	}
	if len(label) > maxLabelLength {
		return fmt.Errorf("customDomain label %q exceeds the maximum length of %d characters", label, maxLabelLength)
	}
	for i, r := range label {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-':
			if i == 0 || i == len(label)-1 {
				return fmt.Errorf("customDomain label %q must not start or end with a hyphen", label)
			}
		default:
			return fmt.Errorf("customDomain label %q contains an invalid character %q", label, r)
		}
	}
	return nil
}
