package webhook

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"time"
)

// Deps is what the two Authenticate functions need from their host operator.
//
// It exists because the extracted originals took the operator's own *Handler and
// called its methods — `h.publicKey(kid)` plus the package-level `writeError`.
// Naming those as an explicit dependency set (rather than an interface the
// operator's Handler must satisfy) keeps the operator's response bytes exactly
// as they were: WriteError is the operator's OWN error writer, so no operator's
// 401 body shape changes as a side effect of this extraction.
type Deps struct {
	// JWKSURL is mitstack's global JWKS endpoint. Empty means "not configured",
	// which is an authentication failure, not a bypass.
	JWKSURL string

	// PublicKey resolves a signing key by kid. Supplied by the operator so it
	// keeps whatever caching it already had.
	PublicKey func(kid string) (ed25519.PublicKey, error)

	// Replay is the nonce/skew guard for body-carrying POSTs. Required for
	// AuthenticateHook; unused by AuthenticateLogsGET, which bounds a signed
	// header timestamp instead.
	Replay *Replay

	// WriteError writes the 401. Defaults to a minimal JSON {"error": msg} body
	// when nil, but every in-repo operator passes its own so response shapes are
	// preserved byte for byte.
	WriteError func(w http.ResponseWriter, status int, msg string)

	// Now allows tests to pin the clock. Defaults to time.Now.
	Now func() time.Time
}

func (d Deps) writeError(w http.ResponseWriter, status int, msg string) {
	if d.WriteError != nil {
		d.WriteError(w, status, msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// AuthenticateHook verifies mitstack's zero-touch Ed25519 signature on the
// webhook body against the platform public key fetched from the global JWKS by
// key id. It returns that public key (for the caller to also verify the OBO
// token) and false if verification failed (a response has already been written).
func AuthenticateHook(d Deps, w http.ResponseWriter, r *http.Request, body []byte) (ed25519.PublicKey, bool) {
	kid := r.Header.Get("X-Mitstack-Key-Id")
	if kid == "" || d.JWKSURL == "" {
		d.writeError(w, http.StatusUnauthorized, "missing X-Mitstack-Key-Id or JWKS not configured")
		return nil, false
	}
	pub, err := d.PublicKey(kid)
	if err != nil {
		d.writeError(w, http.StatusUnauthorized, "cannot resolve mitstack signing key: "+err.Error())
		return nil, false
	}
	if !VerifyEd25519Sig(pub, body, r.Header.Get("X-Mitstack-Signature-Ed25519")) {
		d.writeError(w, http.StatusUnauthorized, "ed25519 signature verification failed")
		return nil, false
	}
	if err := d.Replay.Verify(body, d.now()); err != nil {
		d.writeError(w, http.StatusUnauthorized, "replay check failed: "+err.Error())
		return nil, false
	}
	return pub, true
}

// AuthenticateLogsGET verifies mitstack's Ed25519 signature for a read-only GET
// (e.g. /logs) that carries no request body (T-578). Because a GET has no body,
// it cannot carry the createdAt/nonce envelope the webhook POST path verifies
// via Replay.Verify; instead the engine signs the canonical string
// "GET\n<path-with-query>\n<timestamp>" and sends the anti-replay timestamp in
// the X-Mitstack-Timestamp header. This path verifies that signature and bounds
// the timestamp with the same ReplaySkew window (VerifyTimestampSkew) — the
// webhook POST body-based replay guard is left untouched. It returns the
// platform public key and false (a 401 already written) on failure.
func AuthenticateLogsGET(d Deps, w http.ResponseWriter, r *http.Request) (ed25519.PublicKey, bool) {
	kid := r.Header.Get("X-Mitstack-Key-Id")
	if kid == "" || d.JWKSURL == "" {
		d.writeError(w, http.StatusUnauthorized, "missing X-Mitstack-Key-Id or JWKS not configured")
		return nil, false
	}
	pub, err := d.PublicKey(kid)
	if err != nil {
		d.writeError(w, http.StatusUnauthorized, "cannot resolve mitstack signing key: "+err.Error())
		return nil, false
	}
	ts := r.Header.Get("X-Mitstack-Timestamp")
	if ts == "" {
		d.writeError(w, http.StatusUnauthorized, "missing X-Mitstack-Timestamp")
		return nil, false
	}
	canonical := []byte("GET\n" + r.URL.RequestURI() + "\n" + ts)
	if !VerifyEd25519Sig(pub, canonical, r.Header.Get("X-Mitstack-Signature-Ed25519")) {
		d.writeError(w, http.StatusUnauthorized, "ed25519 signature verification failed")
		return nil, false
	}
	if err := VerifyTimestampSkew(ts, d.now()); err != nil {
		d.writeError(w, http.StatusUnauthorized, "replay check failed: "+err.Error())
		return nil, false
	}
	return pub, true
}
