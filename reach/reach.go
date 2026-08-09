// Package reach answers "is the app serving, and what should the platform
// advertise?" — and, just as importantly, records that answer as a fixed set of
// OUTPUT KEYS every provider of the same capability emits (MIT-1595).
//
// # Why this is a kit package and not three copies
//
// `settle` (MIT-1469) already answers the readiness question as a bool. That is
// enough to decide whether to report `pending`, and the CloudFormation and
// Crossplane twins used it for exactly that. It is NOT enough to be a
// substitutable provider, because the OpenTofu twin does something `settle` has
// no vocabulary for: it writes the verdict onto the subscription's OUTPUTS —
// `reachable`, `certTrusted`, `appHostname` — and the console reads them. The
// "Open app" CTA (MIT-1166) shows only when `reachable` is true, and
// `certTrusted` decides whether that link leads to a browser interstitial.
//
// So the three aws-topology providers agreed on the declared CONTRACT and
// diverged on what they actually returned. Nothing caught it, because a
// schema comparison compares declarations: the twins' schemas were
// byte-identical to the OpenTofu block's while emitting four fewer keys.
// MIT-1439's switch verdict is what finally surfaced it, as four
// `observed-output-undeclared` warnings per target — the app's front door
// going dark on a stack switch, visible only because something finally
// compared observed behaviour rather than declared intent.
//
// A bool cannot carry that. This package is the richer answer, in ONE place,
// for the same reason `webhook` is: a hand-rolled copy beside the dependency is
// precisely the divergence the kit exists to prevent (MIT-1466).
//
// # What it adds over settle.HTTP
//
//   - cert trust reported SEPARATELY from reachability, so a self-signed edge
//     counts as serving with the trust gap reported rather than hidden (T-575);
//   - DNS failure distinguished from "the app is down", because the first is
//     the PLATFORM's fault and is recoverable by probing the box directly
//     (MIT-1291);
//   - an optional diagnosis seam for explaining WHY a name does not resolve
//     (MIT-1300), which the OpenTofu twin fills with a Route 53 delegation
//     check and the others may leave nil;
//   - Apply, which is the whole point: the canonical output keys, written the
//     same way by every provider.
//
// Everything here is pure or seam-injected: no globals, no package-level
// clock, and `Checker` with a nil Probe falls back to the real HTTP probe so a
// test never reaches the network by accident.
package reach

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/mitstack-eu/operator-kit/settle"
)

// ProbeTimeout bounds a single probe. The engine's settle loop re-calls the
// operator, so one probe stays short rather than blocking a webhook while a box
// boots. Exported so an operator's own deadline can be reasoned about against
// it rather than guessed.
const ProbeTimeout = 5 * time.Second

// Output key names. These are the contract: a provider of a capability whose
// consumers read these names must emit ALL of them, or a switch to it silently
// drops a signal the console depends on.
//
// They are constants rather than string literals so `Keys` can enumerate them
// and a parity test can assert every sibling provider agrees — the check that
// would have caught this divergence when it was introduced instead of two
// milestones later.
const (
	// OutputAppURL is the URL the platform advertises. Apply may REWRITE it to
	// the address that actually answered.
	OutputAppURL = "appUrl"
	// OutputReachable is "true"/"false" — the app answered.
	OutputReachable = "reachable"
	// OutputCertTrusted is "true"/"false" — the answer carried a
	// browser-trusted certificate.
	OutputCertTrusted = "certTrusted"
	// OutputAppHostname preserves the INTENDED hostname when appUrl was
	// rewritten to a fallback address.
	OutputAppHostname = "appHostname"
	// OutputPublicIP is the box's stable public address. Written by the
	// provisioner, not by this package — but named here because Evaluate READS
	// it for the fallback probe, so a provider that omits it silently loses
	// MIT-1291's recovery path.
	OutputPublicIP = "publicIp"
)

// Keys returns every output name this package reads or writes, sorted for a
// stable comparison. A capability's providers must all emit this set.
func Keys() []string {
	return []string{
		OutputAppHostname,
		OutputAppURL,
		OutputCertTrusted,
		OutputPublicIP,
		OutputReachable,
	}
}

// Result is what one evaluation learned.
//
// CertTrusted is reported SEPARATELY from Reachable on purpose. An app serving
// a self-signed certificate on a bare-IP URL is serving; treating that as
// unreachable would mark every pre-ACME app dead. A TLS handshake that fails
// only on trust still proves something is listening and speaking TLS, so it
// counts as serving with the trust gap reported rather than hidden.
type Result struct {
	Reachable   bool
	CertTrusted bool

	// Reason is a short, actionable explanation when Reachable is false —
	// surfaced to the consumer instead of a silent false-green. It never
	// carries credentials (the URL is a public address).
	Reason string

	// DNSFailure marks the one failure mode that is the PLATFORM's fault
	// rather than the app's: the advertised hostname does not resolve at all.
	// Distinguishing it from "the app is down" is what lets Evaluate fall back
	// to the address that does work (MIT-1291).
	DNSFailure bool

	// ServingURL is the URL that actually answered — normally the one probed,
	// but the IP fallback when the hostname does not resolve. The caller
	// advertises THIS url, so `active` never promises an address the platform
	// could not reach itself (MIT-1160).
	ServingURL string
}

// Prober performs one bounded probe of a URL. Injected by tests; nil selects
// the real HTTP probe.
type Prober func(ctx context.Context, url string) Result

// Diagnoser explains WHY a hostname does not resolve, in terms the owner can
// act on (e.g. "the apps zone is not delegated"). Optional: an operator with no
// zone to check leaves it nil and loses only the extra sentence, never a
// verdict.
type Diagnoser func(ctx context.Context, hostname string) string

// Checker evaluates reachability for one operator.
//
// The zero value is usable and safe: it probes for real over a default client.
// That is deliberate — an operator that forgets to wire the seam gets the
// correct behaviour, not a vacuous pass.
type Checker struct {
	// Client is the HTTP client for the real probe. Nil selects one bounded by
	// ProbeTimeout. Certificate verification is NEVER disabled — see ProbeURL.
	Client *http.Client
	// Probe overrides the real probe. Tests inject this so both the hostname
	// attempt and the IP fallback are observable.
	Probe Prober
	// Diagnose optionally explains a resolution failure. Nil is fine.
	Diagnose Diagnoser
}

// Evaluate probes the appUrl in a set of outputs and returns the verdict.
//
// It is tolerant in ONE direction only: outputs with no appUrl (nothing to
// probe) are treated as reachable, because a block that reports no URL has no
// serving promise to keep — refusing those would strand every non-web block
// behind a web-only gate. Everything else must actually answer.
func (c Checker) Evaluate(ctx context.Context, outputs map[string]string) Result {
	appURL := outputs[OutputAppURL]
	if appURL == "" {
		return Result{Reachable: true, CertTrusted: false}
	}

	res := c.probe(ctx, appURL)
	if res.Reachable || !res.DNSFailure {
		return res
	}

	// The hostname does not resolve. Find out WHY before anything else: a zone
	// that is not delegated can never resolve, no matter how often the settle
	// loop retries, and that is an owner action rather than a platform bug
	// (MIT-1300). Diagnosing it HERE means the reason names the zone instead of
	// surfacing a bare "no such host".
	diagnosis := c.diagnose(ctx, appURL)

	// The resolution gap is the platform's, not a dead app's — so ask the box
	// directly at the address the provisioner recorded (MIT-1291). If it
	// serves, the app is genuinely up and we advertise the URL we could
	// actually reach.
	ip := outputs[OutputPublicIP]
	if ip == "" {
		return withDiagnosis(res, diagnosis)
	}

	fallbackURL := "https://" + ip + "/"
	fb := c.probe(ctx, fallbackURL)
	if !fb.Reachable {
		// Keep the DNS reason — it is the more actionable of the two.
		return withDiagnosis(res, diagnosis)
	}
	return withDiagnosis(Result{
		Reachable:   true,
		CertTrusted: false, // a bare-IP URL can only carry a self-signed cert
		ServingURL:  fallbackURL,
		Reason: fmt.Sprintf(
			"serving on %s; the app hostname does not resolve yet, so the platform is advertising the box address instead",
			fallbackURL),
	}, diagnosis)
}

func (c Checker) probe(ctx context.Context, url string) Result {
	if c.Probe != nil {
		return c.Probe(ctx, url)
	}
	// The Checker computed this url itself, from outputs it controls — that is
	// the vouching. Passing it as both the probed value and its own sole
	// allowlist entry is what ProbeURL requires before it will dial anything.
	return ProbeURL(ctx, c.Client, url, url)
}

func (c Checker) diagnose(ctx context.Context, hostname string) string {
	if c.Diagnose == nil {
		return ""
	}
	return c.Diagnose(ctx, hostname)
}

// withDiagnosis appends a diagnosis to a result's Reason when there is one.
//
// It never changes the VERDICT: an app serving on its box stays reachable, so
// the fallback keeps converging subscriptions exactly as before. The diagnosis
// only turns an unactionable symptom into a named cause.
func withDiagnosis(res Result, diagnosis string) Result {
	if diagnosis == "" {
		return res
	}
	if res.Reason == "" {
		res.Reason = diagnosis
		return res
	}
	res.Reason += " — " + diagnosis
	return res
}

// PlatformAppsDomain is the platform's own apps zone. It is the one
// implicit entry in TrustedAppURL's allowlist — every app the platform
// provisions gets a subdomain of it.
const PlatformAppsDomain = "apps.mitstack.dev"

// TrustedAppURL is the MIT-1686 barrier for reach.ProbeURL: it validates a
// caller-supplied app URL against an allowed set of domain SUFFIXES — the
// platform's own apps zone plus any operator-supplied extra suffixes — and
// returns a URL rebuilt from the validated components, never the raw input
// string, so a lookalike host can never ride through untouched. It is the
// PRIMARY, unconditional gate: any real production `appUrl` is a subdomain
// of PlatformAppsDomain, so it clears here without needing anything else.
//
// A suffix match, not an equality check against a fixed host list, because
// unlike safe.TrustedBase's GitHub hosts (a handful of known values), every
// app the platform provisions gets its OWN subdomain of apps.mitstack.dev —
// an unbounded set no exact-match allowlist could enumerate.
//
// TrustedAppURL deliberately has NO loopback or address carve-out — a value
// that fails here (a test fixture, the MIT-1291 bare-IP fallback) is not
// automatically unsafe, it is simply outside this function's job. ProbeURL's
// `allowedURLs` parameter (MIT-1683) is where a caller vouches for exactly
// that kind of value instead; composing the two is what keeps this barrier
// meaningful rather than punching a loopback-shaped hole in it.
//
// The single regexp match below covers the WHOLE rebuilt URL — scheme, host
// and path/query together — rather than validating only the parsed host and
// reassembling the rest around it. A host-only check leaves the path and
// query flowing from raw input to the dialled string unexamined, which is a
// request-forgery finding of its own: only a check that constrains the exact
// value handed to the HTTP client counts as a barrier.
func TrustedAppURL(raw string, extraSuffixes ...string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("reach: app URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("reach: app URL %q is not usable", raw)
	}

	host := strings.ToLower(u.Host)
	scheme := strings.ToLower(u.Scheme)
	rebuilt := (&url.URL{Scheme: scheme, Host: host, Path: u.Path, RawQuery: u.RawQuery}).String()

	if trustedAppURLPattern(extraSuffixes).MatchString(rebuilt) {
		return rebuilt, nil
	}
	return "", fmt.Errorf(
		"reach: app URL host %q is not allowlisted for probing — must be https on %s or an operator-supplied suffix",
		host, PlatformAppsDomain)
}

// trustedAppURLPattern compiles the allowlist suffixes (the platform apps
// domain plus any operator-supplied extras) into a single anchored regexp
// matching the exact URL shape TrustedAppURL rebuilds: https on an allowed
// host or subdomain of it, followed by an unconstrained path/query — the
// destination is decided by the host alone, so the path and query never need
// their own check.
//
// A regexp match is used — rather than strings.HasSuffix — because it is
// what CodeQL's go/request-forgery query recognises as a barrier guard for
// the value passed to it; a manual string check does not stop the taint
// analysis from reporting the sink (proven the plain-code way already, twice:
// MIT-1678's safeProbeURL and MIT-1683's equality vouch both left the finding
// open). Go's regexp package (RE2) never backtracks catastrophically, so this
// stays safe on attacker-chosen input.
func trustedAppURLPattern(extraSuffixes []string) *regexp.Regexp {
	suffixes := append([]string{PlatformAppsDomain}, extraSuffixes...)
	var alts []string
	for _, s := range suffixes {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		esc := regexp.QuoteMeta(s)
		// The subdomain label set excludes "/", "?" and "#" on purpose: a
		// greedy match that could cross into the path would let
		// "https://attacker.example.com/foo.apps.mitstack.dev" masquerade as
		// a subdomain of the allowed suffix instead of a foreign host with a
		// deceptive path.
		alts = append(alts, esc, `[a-z0-9.-]+\.`+esc)
	}
	hostAlt := strings.Join(alts, "|")
	return regexp.MustCompile(`^https://(?:` + hostAlt + `)(?::\d+)?(?:/.*)?$`)
}

// ProbeURL performs ONE bounded GET of a public URL and classifies the answer.
//
// Serving means: the edge answered with a status below 500, OR the TLS
// handshake failed only on certificate trust. Not serving means: no URL, a
// disallowed host, a transport failure (nothing listening, timeout, DNS), or
// a 5xx — a 502 is the edge up with the app container down, which is exactly
// the false-green this gate exists to stop. A 4xx IS serving: the consumer's
// own app simply has no route at "/", which is not the platform's business.
//
// appURL arrives from a block's published outputs — data an upstream
// provider, not mitstack, controls — so before any request is built it must
// clear BOTH of two checks, composed with AND, not OR (MIT-1686 + MIT-1683,
// both aimed at the same CodeQL go/request-forgery finding, and an earlier
// version of this composition — reviewed and rejected — used OR):
//
//  1. Vouched: appURL is present, verbatim, in allowedURLs. This is the
//     CALLER asserting "this is the exact URL I computed and want probed."
//  2. Barrier-checked: appURL also clears TrustedAppURL's domain check
//     (MIT-1686 — the platform's own apps domain or an operator-supplied
//     extra suffix) OR is a literal loopback/non-production host (see
//     resolveDialURL's doc comment).
//
// An OR between these two would make the vouch vacuous: every real caller
// (Checker.probe) vouches for appURL WITH ITSELF
// (`ProbeURL(ctx, client, url, url)`), so "vouched OR on an allowed domain"
// degrades to "any https URL a caller is willing to name" the moment
// TrustedAppURL fails to match — which is exactly the SSRF this fix exists
// to close, and precisely how a self-vouched, off-domain request to a cloud
// metadata endpoint (169.254.169.254) would have slipped through. Requiring
// BOTH means a self-vouching caller is constrained by the barrier alone
// (vouching for itself proves nothing new), while the vouch still does real
// work for genuinely two-source calls — a future caller vouching with a URL
// from a source OTHER than appURL narrows what a tainted appURL may match.
//
// One honest, deliberate consequence: a consumer-supplied custom domain
// (aws-app-infra's T-529 `outputs["appUrl"]`, set from a subscribe/upgrade
// request's `domain` param) now fails this barrier and the probe fails
// closed — until a verified-ownership path for that domain exists, it was
// never distinguishable here from an attacker-chosen host, so probing it
// unconditionally was the same class of gap this ticket closes elsewhere.
// That verification is tracked as a follow-up; this function does not grow a
// bypass for it.
//
// Neither the http.Client nor its RoundTripper is ever touched for a value
// that fails either check, and the URL actually dialled is what
// resolveDialURL RETURNS, never appURL itself — see its doc comment for why
// that distinction is what gives a taint tracker something to recognise
// (proven the wrong way twice already: MIT-1678's plain-code validation and
// MIT-1683's bare equality of two untrusted values both left the CodeQL
// finding open, which is why resolveDialURL re-does the barrier through a
// regexp match instead of a plain `==`).
//
// No InsecureSkipVerify anywhere. Trust is classified from the ERROR, via
// settle.IsUntrustedCertificate, so the probe never weakens TLS verification to
// get an answer — a certificate error can only be produced by something that
// completed a handshake, which is precisely the fact being established.
func ProbeURL(ctx context.Context, client *http.Client, appURL string, allowedURLs ...string) Result {
	if appURL == "" {
		return Result{Reason: "no app URL to probe"}
	}

	dialURL, err := resolveDialURL(appURL, allowedURLs)
	if err != nil {
		return Result{Reason: err.Error()}
	}

	if client == nil {
		client = &http.Client{Timeout: ProbeTimeout}
	}

	probeCtx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, dialURL, nil)
	if err != nil {
		return Result{Reason: fmt.Sprintf("app URL is not usable: %v", err)}
	}

	// Never follow a redirect, whatever the injected client is configured to
	// do: a 3xx hop to a second, response-chosen host would let the box being
	// probed steer the request past the URL the caller vouched for. Shallow-copy
	// so the caller's transport and timeout are otherwise untouched.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client = &noRedirect

	resp, err := client.Do(req)
	if err != nil {
		if settle.IsUntrustedCertificate(err) {
			// The box answered a TLS handshake — it is serving; we simply do
			// not trust its (self-signed) certificate.
			return Result{Reachable: true, CertTrusted: false, ServingURL: dialURL}
		}
		if settle.IsDNSFailure(err) {
			return Result{
				DNSFailure: true,
				Reason:     "app hostname does not resolve: " + err.Error(),
			}
		}
		return Result{Reason: "app URL is not answering: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusInternalServerError {
		return Result{
			Reason: fmt.Sprintf("app URL answered %d — the edge is up but the app is not serving yet", resp.StatusCode),
		}
	}
	return Result{Reachable: true, CertTrusted: true, ServingURL: dialURL}
}

// resolveDialURL decides the exact string ProbeURL is allowed to dial: the
// caller's VOUCH (MIT-1683) SELECTS which value is even a candidate, and the
// barrier (MIT-1686) VALIDATES that candidate — the two are ANDed, not
// alternatives. A vouch that isn't also barrier-checked is not a security
// boundary (a self-vouching caller could name anything); a barrier match
// with no vouch would let "it happens to be on the apps domain" substitute
// for "the caller actually wants this dialled" — MIT-1683's own
// default-deny tests already pin that half.
//
// The vouch is checked FIRST and is a plain equality — deliberately not a
// recognised CodeQL barrier on its own, because it isn't the value that
// reaches net/http here; it only decides whether to proceed. dial, the value
// actually returned and later passed to http.NewRequestWithContext, comes
// exclusively from vouchBarrierCheckedURL's regexp-guarded result.
//
// No match — including an empty allowedURLs, or a vouched value that fails
// the barrier — is default-deny: nothing is dialled.
func resolveDialURL(appURL string, allowedURLs []string) (string, error) {
	vouched := false
	for _, candidate := range allowedURLs {
		if candidate == appURL {
			vouched = true
			break
		}
	}
	if !vouched {
		return "", fmt.Errorf(
			"reach: app URL %q is not vouched for by the caller's allowlist (MIT-1683) — TrustedAppURL alone is not sufficient",
			appURL)
	}

	dial, err := vouchBarrierCheckedURL(appURL)
	if err != nil {
		return "", fmt.Errorf(
			"reach: app URL %q is vouched for but clears neither the platform apps domain (%s) nor a loopback/non-production host (MIT-1686): %v",
			appURL, PlatformAppsDomain, err)
	}
	return dial, nil
}

// vouchBarrierCheckedURL is the barrier half of resolveDialURL's AND: a
// vouched candidate is dialled only if it ALSO clears one of two regexp
// checks, tried in order.
//
//  1. TrustedAppURL (MIT-1686) — the platform's own apps domain or an
//     operator-supplied extra suffix. This is what every real production
//     probe clears, vouch or not.
//  2. Failing that, neverRoutableCheckedURL — a literal loopback address
//     (the operator's own box) or one of the RFC 2606/6761 reserved,
//     PERMANENTLY UNDELEGATED test TLDs (".example"/".test"/".invalid",
//     plus bare "localhost"). Neither can ever be a real request-forgery
//     target: no registrar can make ".example" resolve to a host an
//     attacker controls, and a link-local/routable address like the cloud
//     metadata endpoint 169.254.169.254 matches neither alternative, so it
//     is correctly refused even when a caller self-vouches for it.
//
// Both alternatives return a value REBUILT from parsed, regexp-matched
// components — never the raw candidate string — for the same reason
// TrustedAppURL does; see its doc comment.
func vouchBarrierCheckedURL(candidate string) (string, error) {
	if dial, err := TrustedAppURL(candidate); err == nil {
		return dial, nil
	}
	return neverRoutableCheckedURL(candidate)
}

// neverRoutableCheckedURL validates candidate against neverRoutableURLPattern
// and returns the URL rebuilt from its parsed components. It is
// vouchBarrierCheckedURL's second alternative, not a general-purpose relaxed
// check: it exists so an operator's own test/dev fixture (an httptest
// loopback server) and this package's own transport-error classification
// tests (fixed reserved-TLD literals, never a real network target) can be
// vouched for and dialled without widening what a REAL, routable,
// caller-chosen host may reach. The reserved-TLD half assumes the standard
// public DNS root, where these names can never delegate; a host whose own
// resolver is configured to answer for .test/.example (split-horizon DNS)
// weakens that assumption and should not run operators.
func neverRoutableCheckedURL(candidate string) (string, error) {
	u, err := url.Parse(candidate)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("reach: vouched URL %q is not usable", candidate)
	}
	host := strings.ToLower(u.Host)
	scheme := strings.ToLower(u.Scheme)
	rebuilt := (&url.URL{Scheme: scheme, Host: host, Path: u.Path, RawQuery: u.RawQuery}).String()
	if !neverRoutableURLPattern.MatchString(rebuilt) {
		return "", fmt.Errorf("reach: vouched URL %q is not usable", candidate)
	}
	return rebuilt, nil
}

// neverRoutableURLPattern matches loopback (127.0.0.1, [::1], localhost) and
// the RFC 2606/6761 reserved, non-delegatable test TLDs — see
// neverRoutableCheckedURL's doc comment for why both are safe to accept
// regardless of what a caller vouches for.
var neverRoutableURLPattern = regexp.MustCompile(
	`^https?://(?:127\.0\.0\.1|localhost|\[::1\]|(?:example|test|invalid)|[a-z0-9.-]+\.(?:example|test|invalid))(?::\d+)?(?:/.*)?$`)

// Apply records the verdict on the outputs the engine persists, so downstream
// surfaces read a first-class signal instead of re-deriving it.
//
// This is the function that makes providers substitutable. Every aws-topology
// provider calls it, so every one emits the same keys — which is the property
// a stack switch actually depends on, and the one a schema comparison cannot
// check.
//
// It never mutates its argument: the caller's map is copied, because an
// operator that shares an outputs map between the response and its own
// bookkeeping should not have the two drift.
func Apply(outputs map[string]string, res Result) map[string]string {
	merged := make(map[string]string, len(outputs)+len(Keys()))
	for k, v := range outputs {
		merged[k] = v
	}
	merged[OutputReachable] = BoolString(res.Reachable)
	merged[OutputCertTrusted] = BoolString(res.CertTrusted)

	// When the probe had to fall back to the box address, advertise THAT url
	// (MIT-1291). The consumer's "Open app" CTA must lead somewhere that
	// answers; the intended hostname is preserved so the surface can show it,
	// and the next apply promotes it back automatically once the name resolves.
	if res.ServingURL != "" && res.ServingURL != merged[OutputAppURL] {
		merged[OutputAppHostname] = merged[OutputAppURL]
		merged[OutputAppURL] = res.ServingURL
	}
	return merged
}

// BoolString renders a bool as the "true"/"false" the outputs carry. Outputs
// are a string map, so every provider must agree on the spelling — "1"/"yes"
// would read as absent to a console checking for "true".
func BoolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// safeProbeURL parses a caller-supplied app URL and returns the URL to dial,
// rebuilt from the components that were checked.
//
// Only http and https are accepted, and that refusal is the one that matters:
// a file: or gopher: URL turns "probe the consumer's app" into a read of
// whatever the process can reach, and no app URL has ever needed one.
func safeProbeURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("app URL must be http or https")
	}
	if u.Host == "" {
		return "", errors.New("app URL has no host")
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawQuery: u.RawQuery}).String(), nil
}
