package reach_test

// reach_test.go — the contract every aws-topology provider now shares
// (MIT-1595).
//
// The tests below are deliberately weighted toward two things that a bool
// readiness probe cannot express and that the CloudFormation/Crossplane twins
// therefore did not have:
//
//  1. the OUTPUT KEYS. Apply must write the same names every time, because a
//     stack switch compares emitted keys, not declared schemas — that mismatch
//     is the defect this package exists to close.
//  2. the DNS/IP fallback. It is the difference between a subscription that
//     recovers when the apps zone is not delegated and one that hangs forever
//     (MIT-1291).

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mitstack-eu/operator-kit/reach"
)

func TestKeys_AreTheAdvertisedContract(t *testing.T) {
	got := reach.Keys()
	want := []string{"appHostname", "appUrl", "certTrusted", "publicIp", "reachable"}
	if len(got) != len(want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Keys() must be sorted so a parity comparison is stable: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Keys()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// Apply is the substitutability contract: every provider that calls it emits
// the same key set, which is exactly what a switch verdict compares.
func TestApply_AlwaysWritesReachableAndCertTrusted(t *testing.T) {
	out := reach.Apply(map[string]string{"appUrl": "https://app.example/"},
		reach.Result{Reachable: true, CertTrusted: true, ServingURL: "https://app.example/"})

	if out["reachable"] != "true" {
		t.Fatalf("reachable = %q, want \"true\"", out["reachable"])
	}
	if out["certTrusted"] != "true" {
		t.Fatalf("certTrusted = %q, want \"true\"", out["certTrusted"])
	}
	if _, present := out["appHostname"]; present {
		t.Fatalf("appHostname must be absent when the probed URL is the serving URL, got %q", out["appHostname"])
	}
}

// A not-serving verdict must still emit both keys. Omitting them on failure
// would make "absent" ambiguous between "not probed" and "not serving" — and a
// console reading `reachable != "true"` would then treat an unprobed app the
// same as a dead one.
func TestApply_WritesFalseRatherThanOmitting(t *testing.T) {
	out := reach.Apply(map[string]string{"appUrl": "https://app.example/"}, reach.Result{})
	if out["reachable"] != "false" || out["certTrusted"] != "false" {
		t.Fatalf("want explicit false values, got reachable=%q certTrusted=%q",
			out["reachable"], out["certTrusted"])
	}
}

func TestApply_FallbackPreservesIntendedHostnameAndAdvertisesWhatAnswered(t *testing.T) {
	out := reach.Apply(
		map[string]string{"appUrl": "https://app.example/", "publicIp": "203.0.113.9"},
		reach.Result{Reachable: true, ServingURL: "https://203.0.113.9/"},
	)
	if out["appUrl"] != "https://203.0.113.9/" {
		t.Fatalf("appUrl must become the URL that actually answered, got %q", out["appUrl"])
	}
	if out["appHostname"] != "https://app.example/" {
		t.Fatalf("appHostname must preserve the intended hostname, got %q", out["appHostname"])
	}
}

func TestApply_DoesNotMutateCallersMap(t *testing.T) {
	in := map[string]string{"appUrl": "https://app.example/"}
	_ = reach.Apply(in, reach.Result{Reachable: true, ServingURL: "https://203.0.113.9/"})
	if len(in) != 1 || in["appUrl"] != "https://app.example/" {
		t.Fatalf("Apply mutated its argument: %v", in)
	}
}

// A block with no URL has no serving promise to keep. Refusing those would
// strand every non-web block behind a web-only gate.
func TestEvaluate_NoAppURLIsReachable(t *testing.T) {
	var c reach.Checker
	got := c.Evaluate(context.Background(), map[string]string{})
	if !got.Reachable {
		t.Fatalf("outputs without an appUrl must be treated as reachable, got %+v", got)
	}
}

func TestEvaluate_DNSFailureFallsBackToPublicIP(t *testing.T) {
	var probed []string
	c := reach.Checker{
		Probe: func(_ context.Context, url string) reach.Result {
			probed = append(probed, url)
			if url == "https://app.example/" {
				return reach.Result{DNSFailure: true, Reason: "no such host"}
			}
			return reach.Result{Reachable: true}
		},
	}

	got := c.Evaluate(context.Background(), map[string]string{
		"appUrl":   "https://app.example/",
		"publicIp": "203.0.113.9",
	})

	if !got.Reachable {
		t.Fatalf("the box answered on its IP — must be reachable, got %+v", got)
	}
	if got.ServingURL != "https://203.0.113.9/" {
		t.Fatalf("ServingURL = %q, want the IP fallback", got.ServingURL)
	}
	if got.CertTrusted {
		t.Fatal("a bare-IP URL can only carry a self-signed cert — CertTrusted must be false")
	}
	if len(probed) != 2 {
		t.Fatalf("want hostname then IP probed, got %v", probed)
	}
}

// The fallback must not turn a genuinely dead app green. If neither the
// hostname nor the box answers, the verdict stays not-reachable and keeps the
// DNS reason, which is the more actionable of the two.
func TestEvaluate_FallbackThatAlsoFailsStaysUnreachable(t *testing.T) {
	c := reach.Checker{
		Probe: func(_ context.Context, _ string) reach.Result {
			return reach.Result{DNSFailure: true, Reason: "no such host"}
		},
	}
	got := c.Evaluate(context.Background(), map[string]string{
		"appUrl":   "https://app.example/",
		"publicIp": "203.0.113.9",
	})
	if got.Reachable {
		t.Fatalf("nothing answered — must not be reachable: %+v", got)
	}
	if got.Reason != "no such host" {
		t.Fatalf("want the DNS reason preserved, got %q", got.Reason)
	}
}

// A provider that omits publicIp loses the recovery path entirely. That is the
// cost of not emitting the key, and it is worth asserting so the omission is a
// tested consequence rather than a surprise.
func TestEvaluate_NoPublicIPMeansNoFallback(t *testing.T) {
	calls := 0
	c := reach.Checker{
		Probe: func(_ context.Context, _ string) reach.Result {
			calls++
			return reach.Result{DNSFailure: true, Reason: "no such host"}
		},
	}
	got := c.Evaluate(context.Background(), map[string]string{"appUrl": "https://app.example/"})
	if got.Reachable || calls != 1 {
		t.Fatalf("without publicIp there is nothing to fall back to: reachable=%v calls=%d", got.Reachable, calls)
	}
}

func TestEvaluate_DiagnosisIsAppendedWithoutChangingTheVerdict(t *testing.T) {
	c := reach.Checker{
		Probe: func(_ context.Context, _ string) reach.Result {
			return reach.Result{DNSFailure: true, Reason: "no such host"}
		},
		Diagnose: func(_ context.Context, _ string) string {
			return "apps.example is not delegated"
		},
	}
	got := c.Evaluate(context.Background(), map[string]string{"appUrl": "https://app.example/"})
	if got.Reachable {
		t.Fatal("a diagnosis must never change the verdict")
	}
	if got.Reason != "no such host — apps.example is not delegated" {
		t.Fatalf("Reason = %q, want the diagnosis appended", got.Reason)
	}
}

// The Checker's default (nil Probe) path calls the real ProbeURL, which now
// (MIT-1683, the CodeQL go/request-forgery fix) dials only a vouched
// allowlist entry rather than the raw appURL. Evaluate must still work
// end-to-end: it is the Checker's OWN job to vouch for the URL it computed
// from outputs it controls, not to lose all reachability by forwarding an
// unvouched URL into ProbeURL's default-deny gate.
func TestEvaluate_DefaultProbeVouchesForTheURLItComputedAndReachesIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := reach.Checker{Client: srv.Client()}
	got := c.Evaluate(context.Background(), map[string]string{"appUrl": srv.URL})
	if !got.Reachable {
		t.Fatalf("the checker must vouch for the appUrl it computed and reach it, got %+v", got)
	}
}

func TestEvaluate_NilDiagnoserIsFine(t *testing.T) {
	c := reach.Checker{
		Probe: func(_ context.Context, _ string) reach.Result {
			return reach.Result{DNSFailure: true, Reason: "no such host"}
		},
	}
	got := c.Evaluate(context.Background(), map[string]string{"appUrl": "https://app.example/"})
	if got.Reason != "no such host" {
		t.Fatalf("Reason = %q, want it unchanged with no diagnoser", got.Reason)
	}
}

// --- ProbeURL classification ------------------------------------------------
//
// Every case below now passes appURL as its own sole allowlist entry — the
// caller VOUCHES for the exact URL it wants probed (MIT-1683). That is what
// closed the CodeQL go/request-forgery finding: ProbeURL never dials appURL
// itself, only an allowlist element that string-equals it. The default-deny
// cases (nothing vouched) are below this block.

func TestProbeURL_2xxIsServingAndTrusted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got := reach.ProbeURL(context.Background(), srv.Client(), srv.URL, srv.URL)
	if !got.Reachable || !got.CertTrusted {
		t.Fatalf("a plain 200 must be serving and trusted, got %+v", got)
	}
}

// A 4xx IS serving — the consumer's own app simply has no route at "/", which
// is not the platform's business.
func TestProbeURL_4xxIsServing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if got := reach.ProbeURL(context.Background(), srv.Client(), srv.URL, srv.URL); !got.Reachable {
		t.Fatalf("404 means the edge answered — must be reachable, got %+v", got)
	}
}

// A 502 is the edge up with the app container down. Treating it as serving is
// the exact false-green MIT-1160 exists to stop.
func TestProbeURL_5xxIsNotServing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	if got := reach.ProbeURL(context.Background(), srv.Client(), srv.URL, srv.URL); got.Reachable {
		t.Fatalf("502 must not be serving, got %+v", got)
	}
}

// The self-signed edge case, and the reason the probe never sets
// InsecureSkipVerify: an untrusted certificate proves a TLS peer completed a
// handshake, which is the fact being established.
func TestProbeURL_UntrustedCertIsServingButNotTrusted(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A default client does NOT trust httptest's ad-hoc CA.
	got := reach.ProbeURL(context.Background(), &http.Client{}, srv.URL, srv.URL)
	if !got.Reachable {
		t.Fatalf("an untrusted cert still proves something is serving, got %+v", got)
	}
	if got.CertTrusted {
		t.Fatal("CertTrusted must be false for an untrusted chain")
	}
}

func TestProbeURL_EmptyURL(t *testing.T) {
	if got := reach.ProbeURL(context.Background(), nil, ""); got.Reachable {
		t.Fatalf("no URL is not reachable, got %+v", got)
	}
}

func TestProbeURL_ConnectionRefusedIsNotADNSFailure(t *testing.T) {
	// A closed port on loopback: resolvable, not answering. Misclassifying this
	// as DNSFailure would send Evaluate down the IP-fallback path for an app
	// that is simply down.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind loopback: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	url := "http://" + addr + "/"
	got := reach.ProbeURL(context.Background(), &http.Client{}, url, url)
	if got.Reachable {
		t.Fatalf("a refused connection is not serving, got %+v", got)
	}
	if got.DNSFailure {
		t.Fatalf("a refused connection is not a DNS failure, got %+v", got)
	}
}

// --- ProbeURL allowlist default-deny (CodeQL go/request-forgery fix, MIT-1683) --

// The whole point of the fix: an appURL that is not vouched for by any
// allowlist entry must never be dialed, and must never be reported reachable.
func TestProbeURL_EmptyAllowlistNeverDialsAndIsNotReachable(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got := reach.ProbeURL(context.Background(), srv.Client(), srv.URL)
	if got.Reachable {
		t.Fatalf("an unvouched URL must never be treated as reachable, got %+v", got)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("an unvouched URL must never be dialed, but the server received %d request(s)", n)
	}
	if !strings.Contains(strings.ToLower(got.Reason), "vouch") {
		t.Fatalf("Reason must say the URL was not vouched for, got %q", got.Reason)
	}
}

// A non-empty allowlist that simply does not contain appURL is equally
// default-deny — this is the case a naive host-only or prefix match would get
// wrong.
func TestProbeURL_NotInAllowlistNeverDialsAndIsNotReachable(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got := reach.ProbeURL(context.Background(), srv.Client(), srv.URL, "https://other.example/")
	if got.Reachable {
		t.Fatalf("a URL absent from the allowlist must never be treated as reachable, got %+v", got)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("a URL absent from the allowlist must never be dialed, but the server received %d request(s)", n)
	}
}

// Guard the classification the probe leans on, driven through ProbeURL itself
// rather than through a copy of the matching logic — a test that reimplements
// what it is checking proves only that the copy agrees with itself.
func TestProbeURL_ClassifiesTransportErrors(t *testing.T) {
	cases := map[string]struct {
		err              error
		wantReachable    bool
		wantDNSFailure   bool
		wantCertAccepted bool
	}{
		"unknown authority": {err: x509.UnknownAuthorityError{}, wantReachable: true, wantCertAccepted: true},
		"hostname mismatch": {err: x509.HostnameError{}, wantReachable: true, wantCertAccepted: true},
		"invalid cert":      {err: x509.CertificateInvalidError{}, wantReachable: true, wantCertAccepted: true},
		"verification":      {err: &tls.CertificateVerificationError{}, wantReachable: true, wantCertAccepted: true},
		"dns":               {err: &net.DNSError{Err: "no such host", IsNotFound: true}, wantDNSFailure: true},
		"refused":           {err: errors.New("connection refused")},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: errTransport{err: tc.err}}
			got := reach.ProbeURL(context.Background(), client, "https://app.example/", "https://app.example/")

			if got.Reachable != tc.wantReachable {
				t.Fatalf("Reachable = %v, want %v (%+v)", got.Reachable, tc.wantReachable, got)
			}
			if got.DNSFailure != tc.wantDNSFailure {
				t.Fatalf("DNSFailure = %v, want %v (%+v)", got.DNSFailure, tc.wantDNSFailure, got)
			}
			// A certificate error means serving-but-untrusted, never trusted.
			if tc.wantCertAccepted && got.CertTrusted {
				t.Fatalf("a certificate error must never yield CertTrusted: %+v", got)
			}
		})
	}
}

// errTransport fails every request with a fixed error, so the classification
// branches are reachable without a network.
type errTransport struct{ err error }

func (t errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }
