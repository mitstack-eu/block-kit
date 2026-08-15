// verified_custom_domain_test.go — MIT-1688.
//
// TrustedAppURL's allowlist has exactly one implicit entry (PlatformAppsDomain)
// plus whatever `extraSuffixes` a caller supplies — and MIT-1686 left that
// second half genuinely unreachable: nothing in the tree ever calls
// TrustedAppURL with an extra suffix, so a consumer's own custom domain
// (MIT-1805, MIT-1299) clears nothing and its reachability probe fails closed
// even though the app is really serving there.
//
// Widening the allowlist with the raw customDomain param would reopen the
// SSRF MIT-1686 closed, because that param is exactly the attacker-
// controllable string the barrier exists to keep out. VerifiedCustomDomain is
// the proof instead: the operator provisioned the app's own address, so it can
// resolve the claimed domain and check the answer against that address. A
// domain that answers with infrastructure this subscription created is
// controlled by whoever we provisioned it for — a genuine proof, not a
// restatement of the claim.
//
// The resolver is INJECTABLE (the Resolver interface below) so nothing here
// ever touches a real network, and per MIT-1677's measured CodeQL behaviour a
// resolver-based check is what actually clears go/request-forgery — a suffix
// or regexp check on the claim alone does not.
package reach_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/reach"
)

// fakeResolver answers reach.Resolver's single LookupHost method from a fixed
// table (or a fixed error), so a test never reaches a real resolver.
type fakeResolver struct {
	addrs map[string][]string
	err   error
}

func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	addrs, ok := f.addrs[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return addrs, nil
}

// poisonResolver fails the test the instant it is consulted. Used to prove a
// malformed domain is refused before any lookup is even attempted — mirrors
// trusted_url_test.go's failIfCalledTransport for the same reason: the
// refusal must happen at the validation boundary, not merely produce the
// right answer after doing unnecessary (and, in production, real) work.
type poisonResolver struct{ t *testing.T }

func (p poisonResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	p.t.Fatalf("resolver must not be consulted for a malformed domain, got a lookup for %q", host)
	return nil, nil
}

// --- VerifiedCustomDomain: acceptance ---------------------------------------

func TestVerifiedCustomDomain_MatchingAddressVerifies(t *testing.T) {
	resolver := fakeResolver{addrs: map[string][]string{"myapp.example.org": {"203.0.113.10"}}}

	got, err := reach.VerifiedCustomDomain(context.Background(), resolver, "myapp.example.org", []string{"203.0.113.10"})
	if err != nil {
		t.Fatalf("a domain resolving to exactly the expected address must verify, got error: %v", err)
	}
	if got != "myapp.example.org" {
		t.Fatalf("VerifiedCustomDomain() = %q, want the verified domain returned", got)
	}
}

// An app behind more than one A record is a legitimate, ordinary shape (a
// round-robin or multi-AZ setup) — it must not be penalised for having more
// than one address just because this subscription's own EIP is only one of
// them. Verification only needs to find ONE resolved address matching the
// expected set, not require every resolved address to match.
func TestVerifiedCustomDomain_OneOfMultipleResolvedAddressesMatchingVerifies(t *testing.T) {
	resolver := fakeResolver{addrs: map[string][]string{
		"myapp.example.org": {"198.51.100.1", "203.0.113.10", "198.51.100.2"},
	}}

	got, err := reach.VerifiedCustomDomain(context.Background(), resolver, "myapp.example.org", []string{"203.0.113.10"})
	if err != nil {
		t.Fatalf("one matching address among several resolved must still verify, got error: %v", err)
	}
	if got != "myapp.example.org" {
		t.Fatalf("VerifiedCustomDomain() = %q, want the verified domain returned", got)
	}
}

// --- VerifiedCustomDomain: rejection ----------------------------------------

func TestVerifiedCustomDomain_DifferentAddressIsRefused(t *testing.T) {
	resolver := fakeResolver{addrs: map[string][]string{"myapp.example.org": {"198.51.100.99"}}}

	_, err := reach.VerifiedCustomDomain(context.Background(), resolver, "myapp.example.org", []string{"203.0.113.10"})
	if err == nil {
		t.Fatal("a domain resolving to an address OTHER than the expected one must be refused")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "match") {
		t.Fatalf("error %q must say the resolved address did not match", err.Error())
	}
}

func TestVerifiedCustomDomain_NonResolvingDomainIsRefused(t *testing.T) {
	resolver := fakeResolver{} // empty table => every lookup answers NXDOMAIN

	_, err := reach.VerifiedCustomDomain(context.Background(), resolver, "myapp.example.org", []string{"203.0.113.10"})
	if err == nil {
		t.Fatal("a domain that does not resolve at all must be refused")
	}
}

func TestVerifiedCustomDomain_ResolverErrorIsNeverTreatedAsSuccess(t *testing.T) {
	resolver := fakeResolver{err: errors.New("resolver timeout")}

	got, err := reach.VerifiedCustomDomain(context.Background(), resolver, "myapp.example.org", []string{"203.0.113.10"})
	if err == nil {
		t.Fatalf("a resolver error must never be read as a successful verification, got verified=%q", got)
	}
	if got != "" {
		t.Fatalf("a refused verification must return an empty domain, got %q", got)
	}
}

func TestVerifiedCustomDomain_RefusesEmptyDomain(t *testing.T) {
	if _, err := reach.VerifiedCustomDomain(context.Background(), poisonResolver{t}, "", []string{"203.0.113.10"}); err == nil {
		t.Fatal("an empty domain must be refused")
	}
}

func TestVerifiedCustomDomain_RefusesDomainShapedLikeAURL(t *testing.T) {
	if _, err := reach.VerifiedCustomDomain(context.Background(), poisonResolver{t}, "https://myapp.example.org", []string{"203.0.113.10"}); err == nil {
		t.Fatal("a domain carrying a scheme is not a bare hostname and must be refused")
	}
}

func TestVerifiedCustomDomain_RefusesDomainWithPort(t *testing.T) {
	if _, err := reach.VerifiedCustomDomain(context.Background(), poisonResolver{t}, "myapp.example.org:8080", []string{"203.0.113.10"}); err == nil {
		t.Fatal("a domain carrying a port is not a bare hostname and must be refused")
	}
}

func TestVerifiedCustomDomain_RefusesDomainWithPath(t *testing.T) {
	if _, err := reach.VerifiedCustomDomain(context.Background(), poisonResolver{t}, "myapp.example.org/admin", []string{"203.0.113.10"}); err == nil {
		t.Fatal("a domain carrying a path is not a bare hostname and must be refused")
	}
}

// --- The verified domain, composed with TrustedAppURL ----------------------
//
// VerifiedCustomDomain's return value is documented as "suitable for passing
// as an extra suffix" — these tests pin that composition directly, using
// plain.mitstack.dev (the tracker's own custom-domain app, MIT-1299/MIT-1805)
// as the worked example.

func TestVerifiedCustomDomain_ReturnedDomainAllowsItselfAndSubdomainsViaTrustedAppURL(t *testing.T) {
	resolver := fakeResolver{addrs: map[string][]string{"plain.mitstack.dev": {"203.0.113.10"}}}
	verified, err := reach.VerifiedCustomDomain(context.Background(), resolver, "plain.mitstack.dev", []string{"203.0.113.10"})
	if err != nil {
		t.Fatalf("unexpected error verifying plain.mitstack.dev: %v", err)
	}

	if _, err := reach.TrustedAppURL("https://plain.mitstack.dev/", verified); err != nil {
		t.Fatalf("the verified domain itself must clear TrustedAppURL as an extra suffix: %v", err)
	}
	if _, err := reach.TrustedAppURL("https://api.plain.mitstack.dev/", verified); err != nil {
		t.Fatalf("a subdomain of the verified domain must clear TrustedAppURL as an extra suffix: %v", err)
	}
}

// Suffix trickery must still be rejected with the verified domain in place —
// the extra suffix widens the allowlist by exactly one domain (and its real
// subdomains), never by anything a lookalike host can borrow from it.
//
// Each sub-test states whether TrustedAppURL's existing regexp already
// handles the shape (it does, for all four below — the host-only rebuild
// already strips path/userinfo, and the anchored suffix match already
// enforces a dot boundary) so these pin existing behaviour under the new
// suffix rather than exercising a new code path.

func TestVerifiedCustomDomain_SuffixTrickery_TrailingAttackerLabelStillRejected(t *testing.T) {
	resolver := fakeResolver{addrs: map[string][]string{"plain.mitstack.dev": {"203.0.113.10"}}}
	verified, err := reach.VerifiedCustomDomain(context.Background(), resolver, "plain.mitstack.dev", []string{"203.0.113.10"})
	if err != nil {
		t.Fatalf("unexpected error verifying plain.mitstack.dev: %v", err)
	}

	if _, err := reach.TrustedAppURL("https://plain.mitstack.dev.attacker.com/", verified); err == nil {
		t.Fatal("a host that merely ENDS with an attacker label must be refused even with the extra suffix in place")
	}
}

func TestVerifiedCustomDomain_SuffixTrickery_NoDotBoundaryPrefixStillRejected(t *testing.T) {
	resolver := fakeResolver{addrs: map[string][]string{"plain.mitstack.dev": {"203.0.113.10"}}}
	verified, err := reach.VerifiedCustomDomain(context.Background(), resolver, "plain.mitstack.dev", []string{"203.0.113.10"})
	if err != nil {
		t.Fatalf("unexpected error verifying plain.mitstack.dev: %v", err)
	}

	if _, err := reach.TrustedAppURL("https://evilplain.mitstack.dev/", verified); err == nil {
		t.Fatal("a host that is string-suffixed with no dot boundary must be refused even with the extra suffix in place")
	}
}

func TestVerifiedCustomDomain_SuffixTrickery_HostInPathStillRejected(t *testing.T) {
	resolver := fakeResolver{addrs: map[string][]string{"plain.mitstack.dev": {"203.0.113.10"}}}
	verified, err := reach.VerifiedCustomDomain(context.Background(), resolver, "plain.mitstack.dev", []string{"203.0.113.10"})
	if err != nil {
		t.Fatalf("unexpected error verifying plain.mitstack.dev: %v", err)
	}

	if _, err := reach.TrustedAppURL("https://attacker.com/plain.mitstack.dev", verified); err == nil {
		t.Fatal("the verified domain appearing only in the PATH of a foreign host must be refused")
	}
}

func TestVerifiedCustomDomain_SuffixTrickery_UserinfoTrickStillRejected(t *testing.T) {
	resolver := fakeResolver{addrs: map[string][]string{"plain.mitstack.dev": {"203.0.113.10"}}}
	verified, err := reach.VerifiedCustomDomain(context.Background(), resolver, "plain.mitstack.dev", []string{"203.0.113.10"})
	if err != nil {
		t.Fatalf("unexpected error verifying plain.mitstack.dev: %v", err)
	}

	if _, err := reach.TrustedAppURL("https://plain.mitstack.dev@attacker.com/", verified); err == nil {
		t.Fatal("the verified domain used as USERINFO ahead of a foreign host must be refused")
	}
}

// --- No global mutable allowlist --------------------------------------------

// Verification is per-subscription: the extras must be threaded through each
// call, never accumulated into package state. If VerifiedCustomDomain (or
// TrustedAppURL) ever cached a verified domain globally, a SECOND, unrelated
// call — for a different app, carrying no extra suffix of its own — would
// wrongly inherit trust for a domain that has nothing to do with it.
func TestVerifiedCustomDomain_DoesNotBecomeGloballyTrusted(t *testing.T) {
	resolver := fakeResolver{addrs: map[string][]string{"plain.mitstack.dev": {"203.0.113.10"}}}
	verified, err := reach.VerifiedCustomDomain(context.Background(), resolver, "plain.mitstack.dev", []string{"203.0.113.10"})
	if err != nil {
		t.Fatalf("unexpected error verifying plain.mitstack.dev: %v", err)
	}
	if _, err := reach.TrustedAppURL("https://plain.mitstack.dev/", verified); err != nil {
		t.Fatalf("the verified domain must be trusted when explicitly passed as an extra suffix: %v", err)
	}

	// A later, unrelated call for the SAME host but with no extra suffix — the
	// shape every other subscription's probe uses — must be refused exactly
	// as before verification ever happened.
	if _, err := reach.TrustedAppURL("https://plain.mitstack.dev/"); err == nil {
		t.Fatal("a verified domain must not remain trusted once its extra suffix is omitted from the call — there is no global allowlist")
	}
}
