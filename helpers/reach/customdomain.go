package reach

import (
	"fmt"
	"strings"
)

// The presentation-form limits browsers and ACME clients hold a hostname to.
const (
	maxHostnameLength = 253
	maxLabelLength    = 63
)

// ValidateCustomDomain refuses anything but a bare RFC 1035 hostname, since the value reaches an edge's Caddy and ACME config, and accepts empty as the opt-out.
func ValidateCustomDomain(domain string) error {
	if domain == "" {
		return nil
	}
	for _, r := range domain {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("custom domain must not contain control characters (found %q)", r)
		}
	}
	switch {
	case strings.ContainsAny(domain, " \t{}"):
		return fmt.Errorf("custom domain must not contain spaces or braces — it must be a bare hostname, not config")
	case strings.Contains(domain, "://"):
		return fmt.Errorf("custom domain must not include a URL scheme — pass a bare hostname")
	case strings.ContainsAny(domain, "/:"):
		return fmt.Errorf("custom domain must be a bare hostname, not a URL with a path or port")
	case strings.Contains(domain, "*"):
		return fmt.Errorf("custom domain must not be a wildcard")
	case len(domain) > maxHostnameLength:
		return fmt.Errorf("custom domain exceeds the maximum hostname length of %d characters", maxHostnameLength)
	}
	for _, label := range strings.Split(domain, ".") {
		if err := validateLabel(label); err != nil {
			return err
		}
	}
	return nil
}

func validateLabel(label string) error {
	if label == "" {
		return fmt.Errorf("custom domain must not contain an empty label (leading dot, trailing dot, or double dot)")
	}
	if len(label) > maxLabelLength {
		return fmt.Errorf("custom domain label %q exceeds the maximum length of %d characters", label, maxLabelLength)
	}
	for i, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-':
			if i == 0 || i == len(label)-1 {
				return fmt.Errorf("custom domain label %q must not start or end with a hyphen", label)
			}
		default:
			return fmt.Errorf("custom domain label %q contains an invalid character %q", label, r)
		}
	}
	return nil
}
