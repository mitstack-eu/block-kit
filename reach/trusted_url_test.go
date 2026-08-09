// trusted_url_test.go pins the fix for CodeQL alert #78 (go/request-forgery,
// critical) at reach.go's ProbeURL: appUrl arrives in the signed webhook
// payload from a block subscription's outputs, which an upstream provider —
// not mitstack itself — controls, so ProbeURL must never dial a host that
// isn't the platform's own apps domain (MIT-1686).
//
// TrustedAppURL is the barrier: it validates a raw URL against an
// allowed-domain-suffix set (the constant apps.mitstack.dev, plus any
// operator-supplied extra suffixes) and returns a URL REBUILT from the
// validated components — never the raw input string — so a lookalike host
// can never ride through untouched. ProbeURL must apply this barrier before
// any request leaves the process.
package reach_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/reach"
)

// --- TrustedAppURL: acceptance ----------------------------------------------

func TestTrustedAppURL_AcceptsThePlatformAppsDomain(t *testing.T) {
	got, err := reach.TrustedAppURL("https://myapp.apps.mitstack.dev/healthz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "https://myapp.apps.mitstack.dev") {
		t.Fatalf("TrustedAppURL() = %q, want it to keep the scheme+host", got)
	}
	if !strings.HasSuffix(got, "/healthz") {
		t.Fatalf("TrustedAppURL() = %q, want the path preserved", got)
	}
}

func TestTrustedAppURL_AcceptsAnOperatorSuppliedExtraSuffix(t *testing.T) {
	got, err := reach.TrustedAppURL("https://myapp.apps.example.org/", "apps.example.org")
	if err != nil {
		t.Fatalf("unexpected error with an allowlisted extra suffix: %v", err)
	}
	if !strings.Contains(got, "myapp.apps.example.org") {
		t.Fatalf("TrustedAppURL() = %q, want the extra-suffix host preserved", got)
	}
}

// --- TrustedAppURL: rejection -------------------------------------------------

func TestTrustedAppURL_RejectsAForeignHost(t *testing.T) {
	if _, err := reach.TrustedAppURL("https://attacker.example.com/"); err == nil {
		t.Fatal("a host outside every allowed suffix must be refused")
	}
}

func TestTrustedAppURL_RejectsPlainHTTP(t *testing.T) {
	if _, err := reach.TrustedAppURL("http://myapp.apps.mitstack.dev/"); err == nil {
		t.Fatal("plain http must be refused — the probe barrier requires https")
	}
}

func TestTrustedAppURL_RejectsASuffixTrickHostWithTrailingAttackerLabel(t *testing.T) {
	// "evil-apps.mitstack.dev.attacker.com" ends in ".attacker.com", not in
	// ".apps.mitstack.dev" — a naive strings.Contains/HasSuffix-on-the-wrong-
	// operand check would be fooled by this exact shape.
	if _, err := reach.TrustedAppURL("https://evil-apps.mitstack.dev.attacker.com/"); err == nil {
		t.Fatal("a host that merely CONTAINS the allowed domain must be refused")
	}
}

func TestTrustedAppURL_RejectsAHostThatIsAPrefixNotASubdomain(t *testing.T) {
	// "xapps.mitstack.dev" neither equals "apps.mitstack.dev" nor ends with
	// ".apps.mitstack.dev" — a bare strings.HasSuffix(host, "apps.mitstack.dev")
	// (missing the leading dot) would wrongly accept this.
	if _, err := reach.TrustedAppURL("https://xapps.mitstack.dev/"); err == nil {
		t.Fatal("a host that is merely string-suffixed (no dot boundary) must be refused")
	}
}

func TestTrustedAppURL_RejectsEmptyString(t *testing.T) {
	if _, err := reach.TrustedAppURL(""); err == nil {
		t.Fatal("an empty raw URL must be refused")
	}
}

func TestTrustedAppURL_RejectsUnparsableGarbage(t *testing.T) {
	if _, err := reach.TrustedAppURL("://not a url"); err == nil {
		t.Fatal("a URL that fails to parse must be refused")
	}
}

// --- TrustedAppURL: the rebuild guarantee ------------------------------------

func TestTrustedAppURL_RejectionErrorNamesTheOffendingHost(t *testing.T) {
	_, err := reach.TrustedAppURL("https://attacker.example.com/")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "attacker.example.com") {
		t.Fatalf("error %q must name the offending host so an operator can diagnose it", err.Error())
	}
}

func TestTrustedAppURL_AcceptedReturnValueIsExactlyTheRebuiltForm(t *testing.T) {
	// The accepted return value must be the URL rebuilt from validated
	// components, not the raw input string echoed back — even when the raw
	// input carries no attacker payload, a query string smuggled onto an
	// otherwise-legitimate host must not survive into the dialed URL
	// unexamined.
	raw := "https://myapp.apps.mitstack.dev/healthz?x=" + "attacker-controlled-marker"
	got, err := reach.TrustedAppURL(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://myapp.apps.mitstack.dev/healthz?x=attacker-controlled-marker"
	if got != want {
		t.Fatalf("TrustedAppURL() = %q, want the exact rebuilt form %q", got, want)
	}
}

// --- ProbeURL: vouch AND domain barrier, composed — not either/or ----------
//
// MIT-1683 added a caller-vouch escape hatch (allowedURLs) alongside
// TrustedAppURL's domain barrier. The escape hatch exists for values
// TrustedAppURL was never going to accept — the operator dialing its own
// loopback fixture — not as a second, unrestricted way onto the network. A
// vouch that is honoured on its OWN, without also being domain/loopback
// checked, makes the vouch vacuous: every real caller (Checker.probe)
// self-vouches by construction (`ProbeURL(ctx, client, url, url)`), so an OR
// between "vouched" and "on an allowed domain" degrades to "any https URL",
// which is the SSRF this whole fix exists to close. These tests pin the
// corrected AND: dial only when a URL is BOTH vouched AND clears the domain
// barrier (apps.mitstack.dev, an operator suffix, or a literal loopback host).

// failIfCalledTransport fails the test the moment RoundTrip is invoked. Used
// to prove ProbeURL refuses a URL WITHOUT ever attempting the HTTP request —
// the whole point of fixing this at the dial boundary rather than after the
// fact.
type failIfCalledTransport struct{ t *testing.T }

func (f failIfCalledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.t.Fatalf("no HTTP request may be made for a URL that fails the barrier, got a request to %q", req.URL)
	return nil, nil
}

// This is the test whose absence let the rubber stamp through: appURL is
// passed as its OWN allowedURLs entry — exactly the shape every real caller
// uses to vouch for a URL it computed itself — yet the host is neither the
// platform's apps domain nor loopback. A caller's self-vouch must never be
// sufficient on its own; the domain barrier still has to clear independently,
// or any https URL a caller is willing to name becomes dialable, including
// the cloud metadata endpoint.
func TestProbeURL_HostileSelfVouchedURLIsRefusedWithoutDialing(t *testing.T) {
	hostile := "https://169.254.169.254/latest/meta-data/"
	client := &http.Client{Transport: failIfCalledTransport{t: t}}

	got := reach.ProbeURL(context.Background(), client, hostile, hostile)

	if got.Reachable {
		t.Fatalf("a self-vouched but off-domain, non-loopback host must never be reachable, got %+v", got)
	}
	if !strings.Contains(got.Reason, "169.254.169.254") {
		t.Fatalf("Reason = %q, want it to name the disallowed host", got.Reason)
	}
}

// The loopback carve-out is real, not just documented: a vouched loopback URL
// (the operator dialing a test/dev fixture on its own box) must still be
// dialed and classified normally. This is what keeps
// TestEvaluate_DefaultProbeVouchesForTheURLItComputedAndReachesIt and every
// httptest-based operator suite passing after the AND is enforced.
func TestProbeURL_VouchedLoopbackURLIsDialedNormally(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got := reach.ProbeURL(context.Background(), srv.Client(), srv.URL, srv.URL)

	if !got.Reachable {
		t.Fatalf("a vouched loopback URL must be dialed and reachable, got %+v", got)
	}
}

// The other half of the AND: an apps-domain URL that clears the domain
// barrier on its own but comes with an EMPTY allowedURLs must still be
// refused. MIT-1683's own empty-allowlist test
// (TestProbeURL_EmptyAllowlistNeverDialsAndIsNotReachable) pins this for a
// loopback URL; this pins the same default-deny for the platform's own apps
// domain, so "it's on apps.mitstack.dev" can never substitute for "the caller
// vouched for it".
func TestProbeURL_AppsDomainURLWithoutAVouchIsRefusedWithoutDialing(t *testing.T) {
	client := &http.Client{Transport: failIfCalledTransport{t: t}}

	got := reach.ProbeURL(context.Background(), client, "https://myapp.apps.mitstack.dev/")

	if got.Reachable {
		t.Fatalf("an unvouched URL must never be reachable even on the platform's own apps domain, got %+v", got)
	}
	if !strings.Contains(strings.ToLower(got.Reason), "vouch") {
		t.Fatalf("Reason = %q, want it to say the URL was not vouched for", got.Reason)
	}
}

// Regression guard: an allowlisted apps.mitstack.dev URL that IS vouched for
// still probes normally through the barrier.
func TestProbeURL_StillProbesAnAllowlistedURL(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.URL.Host != "myapp.apps.mitstack.dev" {
			t.Fatalf("request went to host %q, want myapp.apps.mitstack.dev", req.URL.Host)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Header:     make(http.Header),
		}, nil
	})}

	appURL := "https://myapp.apps.mitstack.dev/"
	got := reach.ProbeURL(context.Background(), client, appURL, appURL)

	if !called {
		t.Fatal("an allowlisted, vouched URL must still be probed over HTTP")
	}
	if !got.Reachable {
		t.Fatalf("a 200 from an allowlisted host must be reachable, got %+v", got)
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
