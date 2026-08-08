// Package ghapi_test pins the host-resolution contract of kit/ghapi (MIT-1565):
// the GitHub API host is the OPERATOR's decision, never the caller's
// (MIT-1325), extracted once from the six near-duplicate urlguard.go copies
// (examples/web-app-aws/app-scaffold/urlguard.go is the reference).
package ghapi_test

import (
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/ghapi"
)

// --- The legitimate case: resolves to the allowlist entry -------------------

func TestBase_ResolvesLegitimateEndpointToTheAllowlistEntry(t *testing.T) {
	b, err := ghapi.Base("https://api.github.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.String() != "https://api.github.com" {
		t.Errorf("Base() = %q, want the canonical allowlist entry", b.String())
	}
}

func TestBase_ReturnsTheAllowlistEntryNeverTheCallerHost(t *testing.T) {
	// The whole point (MIT-1325 / MIT-1565): a request-supplied endpoint may
	// only be COMPARED, never propagated into the returned Base. Vary the
	// caller's casing/trailing-slash and prove the output is byte-identical
	// every time — it must be built from the constant, not from whatever the
	// caller happened to send.
	for _, endpoint := range []string{
		"https://api.github.com",
		"https://API.GitHub.com",
		"https://API.GITHUB.COM/",
		"https://api.github.com/",
	} {
		b, err := ghapi.Base(endpoint)
		if err != nil {
			t.Fatalf("Base(%q): %v", endpoint, err)
		}
		if b.String() != "https://api.github.com" {
			t.Errorf("Base(%q).String() = %q, want the canonical constant", endpoint, b.String())
		}
		if strings.Contains(b.String(), "API") {
			t.Errorf("Base(%q) leaked the caller's original casing: %q", endpoint, b.String())
		}
	}
}

// --- Hostile / lookalike endpoints -------------------------------------------

func TestBase_RefusesAHostileEndpoint(t *testing.T) {
	if _, err := ghapi.Base("https://evil.example.com"); err == nil {
		t.Fatal("a host not on the operator's allowlist must be refused")
	}
}

func TestBase_RefusesALookalikeHost(t *testing.T) {
	// A host that merely CONTAINS api.github.com as a substring or subdomain
	// must never be treated as the real thing.
	for _, endpoint := range []string{
		"https://api.github.com.evil.example",
		"https://evil.example/api.github.com",
		"https://api.github.com@evil.example",
		"https://notapi.github.com",
	} {
		if _, err := ghapi.Base(endpoint); err == nil {
			t.Errorf("Base(%q) succeeded; a lookalike host must be refused", endpoint)
		}
	}
}

func TestBase_RefusesNonHTTPS(t *testing.T) {
	if _, err := ghapi.Base("http://api.github.com"); err == nil {
		t.Error("a non-https base must be refused — a real installation token would travel in clear")
	}
}

func TestBase_RefusesGarbage(t *testing.T) {
	for _, endpoint := range []string{"", "   ", "not a url", "https://", "ftp://api.github.com"} {
		if _, err := ghapi.Base(endpoint); err == nil {
			t.Errorf("Base(%q) succeeded; want an error", endpoint)
		}
	}
}

// --- Operator-supplied extra hosts (GitHub Enterprise) -----------------------

func TestBase_AcceptsAnOperatorSuppliedExtraHost(t *testing.T) {
	// A GitHub Enterprise deployment opts in via operator CONFIG
	// (MITSTACK_GITHUB_API_HOSTS in the real operator), never via a payload.
	b, err := ghapi.Base("https://ghe.corp.example", "ghe.corp.example")
	if err != nil {
		t.Fatalf("an operator-allowlisted enterprise host must be accepted: %v", err)
	}
	if b.String() != "https://ghe.corp.example" {
		t.Errorf("Base() = %q, want %q", b.String(), "https://ghe.corp.example")
	}
}

func TestBase_RefusesAHostTheOperatorDidNotSupply(t *testing.T) {
	// The exact same host, but this call carries no extraHosts — must be
	// refused. A payload can never assert its own enterprise host.
	if _, err := ghapi.Base("https://ghe.corp.example"); err == nil {
		t.Error("a host the operator never configured must be refused")
	}
}

func TestBase_ExtraHostIsCaseInsensitiveAndOrderIndependent(t *testing.T) {
	b, err := ghapi.Base("https://GHE.Corp.Example", "other.example", "ghe.corp.example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.String() != "https://ghe.corp.example" {
		t.Errorf("Base() = %q", b.String())
	}
}

// --- Private / loopback / link-local / metadata hosts ------------------------

func TestBase_RefusesPrivateLoopbackLinkLocalAndMetadataEndpoints(t *testing.T) {
	// None of these are on the default allowlist, so each must fail — this
	// pins the baseline before the stronger claim below.
	for _, endpoint := range []string{
		"https://10.0.0.5",
		"https://127.0.0.1",
		"https://169.254.169.254",
		"https://[::1]",
		"https://localhost",
		"https://metadata.google.internal",
		"https://192.168.1.1",
		"https://0.0.0.0",
	} {
		if _, err := ghapi.Base(endpoint); err == nil {
			t.Errorf("Base(%q) succeeded; want an error", endpoint)
		}
	}
}

func TestBase_AnOperatorExtraHostCanNeverReopenAnInternalOrMetadataAddress(t *testing.T) {
	// The stronger claim, mirroring urlguard's "an allowlist entry can never
	// re-open an internal address": even when the operator's OWN config names
	// one of these as an extra host, Base must still refuse it. A typo or a
	// compromised env var must not be able to point a real installation
	// token at cloud metadata or an internal address.
	cases := []struct{ host, endpoint string }{
		{"10.0.0.5", "https://10.0.0.5"},
		{"127.0.0.1", "https://127.0.0.1"},
		{"169.254.169.254", "https://169.254.169.254"},
		{"[::1]", "https://[::1]"},
		{"localhost", "https://localhost"},
		{"metadata.google.internal", "https://metadata.google.internal"},
		{"0.0.0.0", "https://0.0.0.0"},
		// A bare IP literal is refused even when it is a perfectly public
		// address — only hostnames are an acceptable GitHub Enterprise shape.
		{"8.8.8.8", "https://8.8.8.8"},
	}
	for _, c := range cases {
		if _, err := ghapi.Base(c.endpoint, c.host); err == nil {
			t.Errorf("Base(%q, extraHosts=%q) succeeded; an operator config value must not reopen an internal/metadata/bare-IP host", c.endpoint, c.host)
		}
	}
}
