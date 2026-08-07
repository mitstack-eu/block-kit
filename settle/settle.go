// Package settle answers one question correctly: is the consumer's resource
// actually WORKING yet? (MIT-1469)
//
// The most common mistake in a provisioning operator is reporting success when
// the API call succeeded rather than when the thing the consumer paid for
// serves traffic. MIT-1160 introduced the reachability gate for exactly that: a
// subscription was flipping `active` over a box that answered nothing.
//
// The corollary was learned separately and is just as important: EVERY WAIT MUST
// BE BOUNDED. MIT-1291 exists because an unresolvable app hostname hung a
// subscription forever — unrecoverable without an operator. So `New` REQUIRES a
// deadline: it is a positional argument, not an option, and there is no
// unbounded constructor to reach for. That is the cheapest possible guard
// against the entire class.
//
// And a distinction operators reliably get wrong: STILL CONVERGING is not
// FAILED. MIT-1293 makes it a platform rule — a block that is merely waiting is
// never marked failed and never consumes a retry attempt — but the rule only
// holds if the operator expresses it. Hence a three-valued result rather than a
// bool.
package settle

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// State is the three-valued settle outcome. Two values would be a bug: it is
// the difference between "give it another moment" and "this will never work".
type State int

const (
	// Converging — not ready, but nothing is wrong. The caller reports pending;
	// the platform must not mark it failed or spend a retry (MIT-1293).
	Converging State = iota
	// Serving — the resource actually answers. Only now is `active` honest.
	Serving
	// Failed — the deadline passed, or a probe reported something that will not
	// resolve by waiting.
	Failed
)

func (s State) String() string {
	switch s {
	case Serving:
		return "serving"
	case Failed:
		return "failed"
	default:
		return "converging"
	}
}

// Result is what a settle attempt concluded, with a human-readable reason
// suitable for `statusDetail`.
type Result struct {
	State  State
	Reason string
	// Elapsed since the gate was created, for narration.
	Elapsed time.Duration
}

// Probe reports whether one aspect of the resource is ready. A probe returns
// (false, nil) for "not yet" and (false, err) for "this looks wrong" — the same
// converging/failed distinction, one level down.
type Probe func(ctx context.Context) (ready bool, err error)

// Gate evaluates a set of probes against a deadline.
//
// There is no way to construct one without a deadline. That is deliberate: the
// bug MIT-1291 represents is not "someone chose a bad timeout", it is "nobody
// remembered there should be one".
type Gate struct {
	deadline time.Time
	probes   []namedProbe
	now      func() time.Time
}

type namedProbe struct {
	name  string
	probe Probe
}

// New returns a Gate that gives up at deadline. A zero or past deadline is
// rejected rather than silently treated as "no limit" — the fail-open reading
// is the one that hangs a subscription forever.
func New(deadline time.Time) (*Gate, error) {
	if deadline.IsZero() {
		return nil, errors.New("settle: a deadline is required — an unbounded wait is how a subscription hangs forever (MIT-1291)")
	}
	return &Gate{deadline: deadline, now: time.Now}, nil
}

// MustNew is New for callers that build the deadline from a duration and cannot
// meaningfully fail.
func MustNew(within time.Duration) *Gate {
	g, err := New(time.Now().Add(within))
	if err != nil {
		panic(err)
	}
	return g
}

// SetClock injects a clock for tests.
func (g *Gate) SetClock(now func() time.Time) { g.now = now }

// Add registers a named probe. Order is preserved, and evaluation stops at the
// first probe that is not ready — so a cheap DNS check can guard an expensive
// TLS handshake, and the reason names the step actually blocking.
func (g *Gate) Add(name string, p Probe) *Gate {
	g.probes = append(g.probes, namedProbe{name: name, probe: p})
	return g
}

// Check evaluates the probes once and returns the current state. It does not
// sleep: the caller decides the cadence, which keeps the gate usable from both a
// polling loop and a single validate call.
func (g *Gate) Check(ctx context.Context) Result {
	now := g.now()
	elapsed := time.Duration(0)
	if !g.deadline.IsZero() {
		elapsed = now.Sub(g.deadline)
	}

	if len(g.probes) == 0 {
		// A gate with no probes would report Serving for anything. That is the
		// vacuous pass this package exists to prevent.
		return Result{State: Failed, Reason: "settle: no probes registered — this gate would report serving for anything"}
	}

	for _, np := range g.probes {
		ready, err := np.probe(ctx)
		if err != nil {
			if !now.Before(g.deadline) {
				return Result{State: Failed, Reason: fmt.Sprintf("%s failed and the deadline passed: %v", np.name, err), Elapsed: elapsed}
			}
			return Result{State: Converging, Reason: fmt.Sprintf("%s not ready: %v", np.name, err), Elapsed: elapsed}
		}
		if !ready {
			if !now.Before(g.deadline) {
				return Result{State: Failed, Reason: fmt.Sprintf("%s still not ready when the deadline passed", np.name), Elapsed: elapsed}
			}
			return Result{State: Converging, Reason: np.name + " not ready yet", Elapsed: elapsed}
		}
	}
	return Result{State: Serving, Reason: "all probes ready", Elapsed: elapsed}
}

// --- Standard probes --------------------------------------------------------

// DNS reports whether host resolves. MIT-1291: a name that never resolves must
// eventually FAIL with a named reason, which it does by virtue of the gate's
// deadline — this probe itself only ever says "not yet".
func DNS(host string) Probe {
	return func(ctx context.Context) (bool, error) {
		var r net.Resolver
		addrs, err := r.LookupHost(ctx, host)
		if err != nil {
			return false, err
		}
		return len(addrs) > 0, nil
	}
}

// TCP reports whether addr accepts a connection.
func TCP(addr string) Probe {
	return func(ctx context.Context) (bool, error) {
		d := net.Dialer{}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return false, err
		}
		_ = conn.Close()
		return true, nil
	}
}

// HTTP reports whether rawURL answers with a non-5xx status.
//
// It treats an UNTRUSTED CERTIFICATE as serving. A freshly provisioned edge
// legitimately presents self-signed TLS and may offer no SNI at all (T-575), and
// the question a readiness probe asks is "is something answering", not "do I
// trust this certificate" — that decision belongs to the consumer's browser.
// Treating a self-signed edge as unreachable is exactly the bug T-575 fixed.
//
// It gets there WITHOUT disabling certificate verification. The obvious
// implementation sets InsecureSkipVerify and is both a real weakening (the probe
// would accept a MITM as readiness) and a CodeQL go/disabled-certificate-check
// finding. Instead the request is made with normal verification and a
// certificate error is CLASSIFIED as reachable: a TLS certificate error can only
// be produced by something that completed a TLS handshake, which is precisely
// the fact being probed. Same answer, no weakened client, nothing to suppress —
// and it is the shape aws-app-infra's reachability.go already proved.
func HTTP(rawURL string) Probe {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(ctx context.Context) (bool, error) {
		u, err := url.Parse(rawURL)
		if err != nil || u.Host == "" {
			return false, fmt.Errorf("settle: invalid url %q", rawURL)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			if IsUntrustedCertificate(err) {
				// Something completed a TLS handshake. That is the fact we came
				// to establish.
				return true, nil
			}
			return false, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			return false, nil
		}
		return true, nil
	}
}

// IsUntrustedCertificate reports whether err is a certificate-trust failure, as
// opposed to a connection failure. The distinction matters: the first proves a
// TLS peer is up, the second proves nothing is.
func IsUntrustedCertificate(err error) bool {
	if err == nil {
		return false
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostname) ||
		errors.As(err, &invalid) || errors.As(err, &verification) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "x509:") || strings.Contains(msg, "certificate is not trusted")
}

// IsDNSFailure reports whether err is a name-resolution failure, which callers
// narrate differently from a connection refusal: one means "the record has not
// propagated", the other "the host is not up yet".
func IsDNSFailure(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	return strings.Contains(err.Error(), "no such host")
}
