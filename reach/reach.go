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
	"fmt"
	"net/http"
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
	return ProbeURL(ctx, c.Client, url)
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

// ProbeURL performs ONE bounded GET of a public URL and classifies the answer.
//
// Serving means: the edge answered with a status below 500, OR the TLS
// handshake failed only on certificate trust. Not serving means: no URL, a
// transport failure (nothing listening, timeout, DNS), or a 5xx — a 502 is the
// edge up with the app container down, which is exactly the false-green this
// gate exists to stop. A 4xx IS serving: the consumer's own app simply has no
// route at "/", which is not the platform's business.
//
// No InsecureSkipVerify anywhere. Trust is classified from the ERROR, via
// settle.IsUntrustedCertificate, so the probe never weakens TLS verification to
// get an answer — a certificate error can only be produced by something that
// completed a handshake, which is precisely the fact being established.
func ProbeURL(ctx context.Context, client *http.Client, appURL string) Result {
	if appURL == "" {
		return Result{Reason: "no app URL to probe"}
	}
	if client == nil {
		client = &http.Client{Timeout: ProbeTimeout}
	}

	probeCtx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, appURL, nil)
	if err != nil {
		return Result{Reason: fmt.Sprintf("app URL is not usable: %v", err)}
	}

	resp, err := client.Do(req)
	if err != nil {
		if settle.IsUntrustedCertificate(err) {
			// The box answered a TLS handshake — it is serving; we simply do
			// not trust its (self-signed) certificate.
			return Result{Reachable: true, CertTrusted: false, ServingURL: appURL}
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
	return Result{Reachable: true, CertTrusted: true, ServingURL: appURL}
}

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
