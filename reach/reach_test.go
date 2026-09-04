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
	"time"

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

/* ─── the fallback's own address must be dialable (MIT-1703) ── */

// Every test above that exercises the fallback injects `Probe`, so the
// fallback URL never reaches the real barrier. That is why a barrier which
// refuses every bare IP shipped without a failing test: the fallback works
// perfectly against a fake probe and is refused by the real one.
//
// The fallback's ONLY purpose is to probe the box's own public address when
// the hostname does not resolve. If the barrier cannot dial a public IP, the
// fallback is unreachable code and an app whose hostname does not resolve can
// never settle.

func TestProbeURL_VouchedPublicIPIsDialledRatherThanRefused(t *testing.T) {
	// 203.0.113.0/24 is TEST-NET-3: routable as far as address classification
	// is concerned, and guaranteed never to answer. So a dial attempt fails —
	// but it must fail as a DIAL, not as a refusal before one.
	const ip = "https://203.0.113.9/"
	got := reach.ProbeURL(context.Background(), &http.Client{Timeout: 300 * time.Millisecond}, ip, ip)

	if got.Reachable {
		t.Fatal("TEST-NET-3 cannot answer — a reachable verdict would mean the probe never ran")
	}
	if strings.Contains(got.Reason, "clears neither") {
		t.Fatalf("the fallback address was refused before any dial, so the fallback is dead code: %s", got.Reason)
	}
}

func TestProbeURL_VouchedCloudMetadataAddressIsStillRefused(t *testing.T) {
	const metadata = "http://169.254.169.254/latest/meta-data/"
	got := reach.ProbeURL(context.Background(), &http.Client{Timeout: 300 * time.Millisecond}, metadata, metadata)

	if got.Reachable {
		t.Fatal("the cloud metadata endpoint must never be reachable through this probe")
	}
	if !strings.Contains(got.Reason, "clears neither") {
		t.Fatalf("link-local must be refused BEFORE a dial, whatever the caller vouches for; got %q", got.Reason)
	}
}

func TestProbeURL_VouchedPrivateAndUnspecifiedAddressesAreStillRefused(t *testing.T) {
	for _, u := range []string{
		"https://10.0.0.5/",
		"https://192.168.1.10/",
		"https://172.16.4.4/",
		"https://0.0.0.0/",
		"https://[fe80::1]/",
		"https://[fd00::1]/",
	} {
		got := reach.ProbeURL(context.Background(), &http.Client{Timeout: 300 * time.Millisecond}, u, u)
		if !strings.Contains(got.Reason, "clears neither") {
			t.Errorf("%s must be refused before a dial; got %q", u, got.Reason)
		}
	}
}

func TestProbeURL_UnvouchedPublicIPIsStillRefused(t *testing.T) {
	got := reach.ProbeURL(context.Background(), &http.Client{Timeout: 300 * time.Millisecond},
		"https://203.0.113.9/", "https://203.0.113.10/")

	if !strings.Contains(got.Reason, "not vouched for") {
		t.Fatalf("the vouch requirement is unchanged — an unvouched address is refused first; got %q", got.Reason)
	}
}

// recordingTransport reports which URLs actually reached the network. The
// barrier refuses BEFORE the transport is invoked, so "was it dialled" is the
// only observable that distinguishes a refusal from a failed connection —
// asserting on the returned Reason cannot, because Evaluate deliberately keeps
// the more actionable DNS reason when the fallback also fails.
type recordingTransport struct{ dialled []string }

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.dialled = append(rt.dialled, req.URL.String())
	// The hostname must fail the way a real unresolvable name does, or Evaluate
	// returns before the fallback and the test proves nothing.
	if host := req.URL.Hostname(); net.ParseIP(host) == nil {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return nil, errors.New("no listener in this test")
}

// The end-to-end shape: no injected Probe, so Evaluate runs the real barrier
// on both the hostname and the fallback. The verdict is not the point — that
// the fallback was ATTEMPTED is.
func TestEvaluate_FallbackIsActuallyDialledThroughTheRealProbe(t *testing.T) {
	rt := &recordingTransport{}
	c := reach.Checker{Client: &http.Client{Transport: rt, Timeout: 300 * time.Millisecond}}

	c.Evaluate(context.Background(), map[string]string{
		"appUrl":   "https://nothing.invalid/",
		"publicIp": "203.0.113.9",
	})

	var sawFallback bool
	for _, u := range rt.dialled {
		if strings.Contains(u, "203.0.113.9") {
			sawFallback = true
		}
	}
	if !sawFallback {
		t.Fatalf("the box address was never dialled, so the fallback is dead code; dialled=%v", rt.dialled)
	}
}

/* ─── the hostname must be promoted back once it resolves (MIT-2618) ── */
//
// Apply's own comment claims "the next apply promotes it back automatically
// once the name resolves". Nothing did: Evaluate returns at its first `if`
// the instant the box address it was handed answers, and appHostname is never
// even read. faswq's app kept https://52.57.246.147/ advertised forever after
// one DNS window, and every consumer inherited it — a delivery check building
// a URL no certificate can match, the console's "Open app" link leading to an
// interstitial. These pin the fix as the shared contract every provider of
// this capability gets from the one implementation (MIT-1595): there is
// nothing left in this tree to wire a fix into per-operator, so this suite
// IS the parity check for all three.

// The incident's exact shape: appUrl already rewritten to the box address,
// appHostname carrying the intended name, and the name now resolves and
// serves. It must be advertised again — not kept as a bare address for the
// app's lifetime.
func TestEvaluate_PromotesBackToHostnameOnceItServes(t *testing.T) {
	var probed []string
	c := reach.Checker{
		Probe: func(_ context.Context, url string) reach.Result {
			probed = append(probed, url)
			switch url {
			case "https://52.57.246.147/":
				return reach.Result{Reachable: true, EdgeUp: true, CertTrusted: false}
			case "https://app.example/":
				return reach.Result{Reachable: true, EdgeUp: true, CertTrusted: true}
			}
			t.Fatalf("unexpected probe of %q", url)
			return reach.Result{}
		},
	}

	got := c.Evaluate(context.Background(), map[string]string{
		"appUrl":      "https://52.57.246.147/",
		"appHostname": "https://app.example/",
		"publicIp":    "52.57.246.147",
	})

	if !got.Reachable {
		t.Fatalf("the hostname is serving — must be reachable, got %+v", got)
	}
	if got.ServingURL != "https://app.example/" {
		t.Fatalf("ServingURL = %q, want the hostname promoted back once it resolves and serves", got.ServingURL)
	}
	if !got.CertTrusted {
		t.Fatal("the hostname's own probe reported a trusted certificate — CertTrusted must follow the promoted address, not the box fallback's false")
	}

	var sawHostname bool
	for _, u := range probed {
		if u == "https://app.example/" {
			sawHostname = true
		}
	}
	if !sawHostname {
		t.Fatalf("the hostname was never actually probed, so the promotion is a guess rather than a verified promote; probed=%v", probed)
	}
}

// The hostname resolving is not enough — it has to actually serve. A 5xx or a
// refused connection at the name must leave the box address advertised,
// exactly as before promotion existed.
func TestEvaluate_HostnameRespondsButNotServing_StaysOnBoxAddress(t *testing.T) {
	c := reach.Checker{
		Probe: func(_ context.Context, url string) reach.Result {
			switch url {
			case "https://52.57.246.147/":
				return reach.Result{Reachable: true, EdgeUp: true}
			case "https://app.example/":
				// The edge answers — DNS is fine now — but with a 5xx: the app
				// itself is not serving there yet.
				return reach.Result{Reachable: false, EdgeUp: true}
			}
			t.Fatalf("unexpected probe of %q", url)
			return reach.Result{}
		},
	}

	got := c.Evaluate(context.Background(), map[string]string{
		"appUrl":      "https://52.57.246.147/",
		"appHostname": "https://app.example/",
		"publicIp":    "52.57.246.147",
	})

	if !got.Reachable {
		t.Fatalf("the box is still serving — must stay reachable, got %+v", got)
	}
	if got.ServingURL != "" {
		t.Fatalf("no promotion should happen — ServingURL must stay empty (appUrl unchanged), got %q", got.ServingURL)
	}
}

// The hostname still not resolving at all must leave the box address
// advertised AND keep explaining why, so a consumer reading only the verdict
// still learns the platform is on a fallback rather than reading a bare,
// unexplained success.
func TestEvaluate_HostnameStillUnresolved_StaysOnBoxAddressAndKeepsReason(t *testing.T) {
	c := reach.Checker{
		Probe: func(_ context.Context, url string) reach.Result {
			switch url {
			case "https://52.57.246.147/":
				// A real dial to a serving box carries no Reason of its own —
				// any explanation for why the platform is STILL on this
				// address has to come from the promotion check itself.
				return reach.Result{Reachable: true, EdgeUp: true}
			case "https://app.example/":
				return reach.Result{DNSFailure: true, Reason: "no such host"}
			}
			t.Fatalf("unexpected probe of %q", url)
			return reach.Result{}
		},
	}

	got := c.Evaluate(context.Background(), map[string]string{
		"appUrl":      "https://52.57.246.147/",
		"appHostname": "https://app.example/",
		"publicIp":    "52.57.246.147",
	})

	if !got.Reachable {
		t.Fatalf("the box is still serving — must stay reachable, got %+v", got)
	}
	if got.ServingURL != "" {
		t.Fatalf("no promotion — the hostname still doesn't resolve, got ServingURL=%q", got.ServingURL)
	}
	if got.Reason == "" || !strings.Contains(got.Reason, "does not resolve") {
		t.Fatalf("Reason = %q, want it to keep explaining that the hostname still does not resolve", got.Reason)
	}
}

// appHostname absent is the common, healthy-app path — today's behaviour must
// not change at all, and in particular must not gain a second outbound call
// per Evaluate: a promotion check that runs when there is nothing to promote
// would double every health poll's outbound calls.
func TestEvaluate_NoAppHostname_HealthyPathProbesExactlyOnce(t *testing.T) {
	calls := 0
	c := reach.Checker{
		Probe: func(_ context.Context, _ string) reach.Result {
			calls++
			return reach.Result{Reachable: true, EdgeUp: true, CertTrusted: true}
		},
	}
	got := c.Evaluate(context.Background(), map[string]string{"appUrl": "https://app.example/"})

	if !got.Reachable || !got.CertTrusted {
		t.Fatalf("a directly-serving app must stay reachable and trusted, got %+v", got)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want exactly 1 — nothing to promote must not add a probe", calls)
	}
}

// appHostname equal to appUrl is the no-op shape (nothing was ever rewritten,
// or a caller set both to the same value) — it must not be treated as
// something to promote, and must not add a dial or rewrite the outputs.
func TestEvaluate_AppHostnameEqualsAppURL_NoPromotionNoChurn(t *testing.T) {
	calls := 0
	c := reach.Checker{
		Probe: func(_ context.Context, _ string) reach.Result {
			calls++
			return reach.Result{Reachable: true, EdgeUp: true, CertTrusted: true}
		},
	}
	got := c.Evaluate(context.Background(), map[string]string{
		"appUrl":      "https://app.example/",
		"appHostname": "https://app.example/",
	})

	if calls != 1 {
		t.Fatalf("calls = %d, want exactly 1 — appHostname equal to appUrl is nothing to promote", calls)
	}
	if got.ServingURL != "" {
		t.Fatalf("ServingURL = %q, want empty — no churn when there is nothing to promote", got.ServingURL)
	}
}

// The trust flag must be RECOMPUTED from the hostname's own probe, not
// carried over from the fallback's false. A hostname that resolves and serves
// but still carries a self-signed certificate must still promote — promotion
// is about reachability — while correctly reporting the cert as untrusted,
// proving the false here comes from the hostname's own probe and not merely
// a hardcoded carry-over from the box fallback.
func TestEvaluate_PromotionRecomputesCertTrusted_UntrustedHostnameCert(t *testing.T) {
	c := reach.Checker{
		Probe: func(_ context.Context, url string) reach.Result {
			switch url {
			case "https://52.57.246.147/":
				return reach.Result{Reachable: true, EdgeUp: true, CertTrusted: false}
			case "https://app.example/":
				return reach.Result{Reachable: true, EdgeUp: true, CertTrusted: false}
			}
			t.Fatalf("unexpected probe of %q", url)
			return reach.Result{}
		},
	}
	got := c.Evaluate(context.Background(), map[string]string{
		"appUrl":      "https://52.57.246.147/",
		"appHostname": "https://app.example/",
		"publicIp":    "52.57.246.147",
	})

	if got.ServingURL != "https://app.example/" {
		t.Fatalf("the hostname serves — it must still be promoted even without a trusted cert, got ServingURL=%q", got.ServingURL)
	}
	if got.CertTrusted {
		t.Fatal("the hostname's own certificate is untrusted — promotion must not claim trust it did not observe")
	}
}

// Multi-tenancy: Checker is a value type reused across every Evaluate call in
// the settle loop, so the promotion probe it adds must read only from the
// outputs map it was handed. One app's hostname must never be probed for
// another's outputs.
func TestEvaluate_PromotionNeverProbesAnotherAppsHostname(t *testing.T) {
	var probedForA, probedForB []string

	var c reach.Checker

	c.Probe = func(_ context.Context, url string) reach.Result {
		probedForA = append(probedForA, url)
		switch url {
		case "https://203.0.113.9/", "https://a.example/":
			return reach.Result{Reachable: true, EdgeUp: true, CertTrusted: true}
		}
		t.Fatalf("app A's evaluate probed %q, which belongs to app B", url)
		return reach.Result{}
	}
	c.Evaluate(context.Background(), map[string]string{
		"appUrl":      "https://203.0.113.9/",
		"appHostname": "https://a.example/",
		"publicIp":    "203.0.113.9",
	})

	c.Probe = func(_ context.Context, url string) reach.Result {
		probedForB = append(probedForB, url)
		switch url {
		case "https://198.51.100.7/", "https://b.example/":
			return reach.Result{Reachable: true, EdgeUp: true, CertTrusted: true}
		}
		t.Fatalf("app B's evaluate probed %q, which belongs to app A", url)
		return reach.Result{}
	}
	c.Evaluate(context.Background(), map[string]string{
		"appUrl":      "https://198.51.100.7/",
		"appHostname": "https://b.example/",
		"publicIp":    "198.51.100.7",
	})

	for _, u := range probedForA {
		if strings.Contains(u, "b.example") || strings.Contains(u, "198.51.100.7") {
			t.Fatalf("app A's evaluate probed app B's address: %v", probedForA)
		}
	}
	for _, u := range probedForB {
		if strings.Contains(u, "a.example") || strings.Contains(u, "203.0.113.9") {
			t.Fatalf("app B's evaluate probed app A's address: %v", probedForB)
		}
	}
}

// End to end: once Evaluate reports a promotion, Apply must advertise the
// hostname again — and must not leave a stale duplicate behind. Decision
// pinned here: appHostname existed only to preserve the intended name while
// appUrl pointed at the box: once appUrl IS that name again, the field is
// removed rather than left duplicating appUrl or — worse, under the
// unfixed rewrite rule that treats any ServingURL change as a fresh
// fallback — overwritten with the box address it just left.
func TestApply_PromotionAdvertisesHostnameAndLeavesNoStaleDuplicate(t *testing.T) {
	outputs := map[string]string{
		"appUrl":      "https://52.57.246.147/",
		"appHostname": "https://app.example/",
		"publicIp":    "52.57.246.147",
	}
	res := reach.Result{Reachable: true, CertTrusted: true, ServingURL: "https://app.example/"}

	out := reach.Apply(outputs, res)

	if out["appUrl"] != "https://app.example/" {
		t.Fatalf("appUrl = %q, want the promoted hostname advertised again", out["appUrl"])
	}
	if v, present := out["appHostname"]; present {
		t.Fatalf("appHostname must not survive a promotion (no stale duplicate, and never the old box address), got %q", v)
	}
}
