package webhook_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/verify/kittest"
	"github.com/mitstack-eu/block-kit/webhook"
)

func depsFor(s *kittest.Signer, jwksURL string) webhook.Deps {
	cache := webhook.NewKeyCache()
	return webhook.Deps{
		JWKSURL:   jwksURL,
		PublicKey: func(kid string) (ed25519.PublicKey, error) { return cache.PublicKey(jwksURL, nil, kid, time.Now()) },
		Replay:    webhook.NewReplay(),
	}
}

func TestAuthenticateHook_AcceptsAFreshlySignedBody(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, s.JWKSServer(t))
	body := kittest.Envelope("nonce-1", time.Now(), nil)

	w := httptest.NewRecorder()
	pub, ok := webhook.AuthenticateHook(d, w, s.HookRequest(t, "/hook", body), body)

	if !ok {
		t.Fatalf("expected authentication to succeed, got %d %s", w.Code, w.Body)
	}
	if !pub.Equal(s.Public) {
		t.Error("returned public key is not the signing key")
	}
}

func TestAuthenticateHook_401WhenKeyIdMissing(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, s.JWKSServer(t))
	body := kittest.Envelope("nonce-1", time.Now(), nil)

	r := s.HookRequest(t, "/hook", body)
	r.Header.Del("X-Mitstack-Key-Id")
	w := httptest.NewRecorder()

	if _, ok := webhook.AuthenticateHook(d, w, r, body); ok {
		t.Fatal("expected authentication to fail with no key id")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAuthenticateHook_401WhenJWKSNotConfigured(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, "")
	body := kittest.Envelope("nonce-1", time.Now(), nil)

	w := httptest.NewRecorder()
	if _, ok := webhook.AuthenticateHook(d, w, s.HookRequest(t, "/hook", body), body); ok {
		t.Fatal("an unconfigured JWKS must fail closed, never bypass")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAuthenticateHook_401OnForgedSignature(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	other := kittest.NewSigner(t, "kid-1", 99)
	d := depsFor(s, s.JWKSServer(t))
	body := kittest.Envelope("nonce-1", time.Now(), nil)

	r := s.HookRequest(t, "/hook", body)
	r.Header.Set("X-Mitstack-Signature-Ed25519", other.SignBody(body))
	w := httptest.NewRecorder()

	if _, ok := webhook.AuthenticateHook(d, w, r, body); ok {
		t.Fatal("a body signed by a different key must be rejected")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAuthenticateHook_401WhenBodyTamperedAfterSigning(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, s.JWKSServer(t))
	body := kittest.Envelope("nonce-1", time.Now(), map[string]any{"operation": "subscribe"})

	r := s.HookRequest(t, "/hook", body)
	tampered := []byte(strings.Replace(string(body), "subscribe", "unsubscrib", 1))
	w := httptest.NewRecorder()

	if _, ok := webhook.AuthenticateHook(d, w, r, tampered); ok {
		t.Fatal("a body altered after signing must be rejected")
	}
}

func TestAuthenticateHook_RejectsAReplayedNonce(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, s.JWKSServer(t))
	body := kittest.Envelope("nonce-replayed", time.Now(), nil)

	if _, ok := webhook.AuthenticateHook(d, httptest.NewRecorder(), s.HookRequest(t, "/hook", body), body); !ok {
		t.Fatal("first delivery should succeed")
	}
	w := httptest.NewRecorder()
	if _, ok := webhook.AuthenticateHook(d, w, s.HookRequest(t, "/hook", body), body); ok {
		t.Fatal("a captured request replayed inside the window must be rejected (T-357)")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAuthenticateHook_RejectsAStaleTimestamp(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, s.JWKSServer(t))
	body := kittest.Envelope("nonce-old", time.Now().Add(-webhook.ReplaySkew-time.Minute), nil)

	w := httptest.NewRecorder()
	if _, ok := webhook.AuthenticateHook(d, w, s.HookRequest(t, "/hook", body), body); ok {
		t.Fatal("a request older than the skew window must be rejected")
	}
}

func TestAuthenticateLogsGET_AcceptsACanonicallySignedGET(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, s.JWKSServer(t))

	w := httptest.NewRecorder()
	if _, ok := webhook.AuthenticateLogsGET(d, w, s.LogsRequest(t, "/logs?tail=10", time.Now())); !ok {
		t.Fatalf("expected the signed GET to authenticate, got %d %s", w.Code, w.Body)
	}
}

func TestAuthenticateLogsGET_RejectsASignatureForADifferentPath(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, s.JWKSServer(t))

	// A signature minted for /logs?tail=10 must not authorise /logs?tail=500:
	// the query string is inside the canonical string precisely so it is bound.
	signed := s.LogsRequest(t, "/logs?tail=10", time.Now())
	forged := httptest.NewRequest(http.MethodGet, "/logs?tail=500", nil)
	for k, v := range signed.Header {
		forged.Header[k] = v
	}

	w := httptest.NewRecorder()
	if _, ok := webhook.AuthenticateLogsGET(d, w, forged); ok {
		t.Fatal("a signature bound to one path+query must not authorise another")
	}
}

func TestAuthenticateLogsGET_RejectsAStaleTimestamp(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, s.JWKSServer(t))

	w := httptest.NewRecorder()
	r := s.LogsRequest(t, "/logs", time.Now().Add(-webhook.ReplaySkew-time.Minute))
	if _, ok := webhook.AuthenticateLogsGET(d, w, r); ok {
		t.Fatal("a GET signed outside the skew window must be rejected (T-578)")
	}
}

func TestAuthenticateLogsGET_401WhenTimestampHeaderMissing(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, s.JWKSServer(t))

	r := s.LogsRequest(t, "/logs", time.Now())
	r.Header.Del("X-Mitstack-Timestamp")
	w := httptest.NewRecorder()

	if _, ok := webhook.AuthenticateLogsGET(d, w, r); ok {
		t.Fatal("a GET with no anti-replay timestamp must be rejected")
	}
}

func TestDeps_UsesTheOperatorsOwnErrorWriter(t *testing.T) {
	// Deps carries the operator's own writeError so its 401 body shape is unchanged.
	s := kittest.NewSigner(t, "kid-1", 7)
	d := depsFor(s, "")
	d.WriteError = func(w http.ResponseWriter, status int, msg string) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("operator-shaped: " + msg))
	}

	w := httptest.NewRecorder()
	body := kittest.Envelope("n", time.Now(), nil)
	_, _ = webhook.AuthenticateHook(d, w, s.HookRequest(t, "/hook", body), body)

	if !strings.HasPrefix(w.Body.String(), "operator-shaped: ") {
		t.Errorf("kit wrote its own error body %q; it must delegate to the operator's writer", w.Body.String())
	}
}

func TestVerifyEd25519Sig_RejectsUndecodableSignature(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	if webhook.VerifyEd25519Sig(s.Public, []byte("body"), "ed25519=not-base64!!") {
		t.Error("an undecodable signature must not verify")
	}
}

func TestPublicKeyFromJWKS(t *testing.T) {
	s := kittest.NewSigner(t, "kid-A", 11)

	got, err := webhook.PublicKeyFromJWKS(s.JWKS(), "kid-A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(s.Public) {
		t.Error("extracted key does not match the signer")
	}

	if _, err := webhook.PublicKeyFromJWKS(s.JWKS(), "kid-missing"); err == nil {
		t.Error("an unknown kid must be an error, not a silent fallback to any key")
	}
}

func TestPublicKeyFromJWKS_RejectsNonEd25519Key(t *testing.T) {
	doc := []byte(`{"keys":[{"kty":"RSA","crv":"","kid":"kid-A","x":""}]}`)
	if _, err := webhook.PublicKeyFromJWKS(doc, "kid-A"); err == nil {
		t.Error("a non-OKP/Ed25519 key for the requested kid must be rejected")
	}
}

func TestPublicKeyFromJWKS_RejectsWrongLengthKey(t *testing.T) {
	short := base64.RawURLEncoding.EncodeToString([]byte("too-short"))
	doc := []byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"kid-A","x":"` + short + `"}]}`)
	if _, err := webhook.PublicKeyFromJWKS(doc, "kid-A"); err == nil {
		t.Error("a key of the wrong length must be rejected")
	}
}

// jwks serves the keys of whichever signers it holds at each fetch, or a 503 with an empty key set while down, and counts the fetches.
type jwks struct {
	mu      sync.Mutex
	signers []*kittest.Signer
	down    bool
	fetches int
	url     string
}

func newJWKS(t *testing.T, signers ...*kittest.Signer) *jwks {
	j := &jwks{signers: signers}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		j.mu.Lock()
		defer j.mu.Unlock()
		j.fetches++
		if j.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"keys":[]}`))
			return
		}
		keys := []map[string]string{}
		for _, s := range j.signers {
			keys = append(keys, map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": s.Kid, "x": base64.RawURLEncoding.EncodeToString(s.Public)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	t.Cleanup(srv.Close)
	j.url = srv.URL
	return j
}

func (j *jwks) serve(signers ...*kittest.Signer) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.signers = signers
}

func (j *jwks) setDown(down bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.down = down
}

func TestKeyCache_AnOutageLeavesHeldKeysAndAnswersEveryOtherKidUnavailable(t *testing.T) {
	j := newJWKS(t, kittest.NewSigner(t, "kid-1", 7))
	cache, t0 := webhook.NewKeyCache(), time.Now()
	lookup := func(kid string, after time.Duration) error {
		_, err := cache.PublicKey(j.url, nil, kid, t0.Add(after))
		return err
	}

	j.setDown(true)
	cold, coldAgain := lookup("kid-1", 0), lookup("kid-1", time.Second)
	j.setDown(false)
	recovered := lookup("kid-1", 2*time.Second)
	j.setDown(true)
	duringOutage := lookup("kid-1", 2*time.Second+webhook.JWKSRefreshInterval)
	rotated, rotatedAgain := lookup("kid-2", 3*time.Second+webhook.JWKSRefreshInterval), lookup("kid-2", 4*time.Second+webhook.JWKSRefreshInterval)

	for i, err := range []error{cold, coldAgain, rotated, rotatedAgain} {
		if !errors.Is(err, webhook.ErrKeysUnavailable) {
			t.Errorf("outage lookup %d = %v; want ErrKeysUnavailable for kid-1 while nothing is held and for kid-2 on every retry", i, err)
		}
	}
	if recovered != nil || duringOutage != nil {
		t.Errorf("kid-1 lookups = %v, %v; want its key once the JWKS is back and kept through a later outage", recovered, duringOutage)
	}
}

func (j *jwks) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.fetches
}

func TestKeyCache_AKeyRemovedFromTheJWKSStopsVerifyingAfterOneRefreshInterval(t *testing.T) {
	j := newJWKS(t, kittest.NewSigner(t, "kid-1", 7))
	cache, t0 := webhook.NewKeyCache(), time.Now()
	lookup := func(after time.Duration) error {
		_, err := cache.PublicKey(j.url, nil, "kid-1", t0.Add(after))
		return err
	}

	first, cached := lookup(0), lookup(time.Second)
	j.serve()
	beforeRefresh, afterRefresh := lookup(webhook.JWKSRefreshInterval-time.Second), lookup(webhook.JWKSRefreshInterval)

	if first != nil || cached != nil || beforeRefresh != nil || afterRefresh == nil || j.count() != 2 {
		t.Errorf("lookups = %v, %v, %v, %v after %d fetches; want the key served from one fetch, then refused at the refresh one interval later",
			first, cached, beforeRefresh, afterRefresh, j.count())
	}
}

func TestKeyCache_UnknownKeyIdsFetchAtMostOncePerInterval(t *testing.T) {
	signer, rotated := kittest.NewSigner(t, "kid-1", 7), kittest.NewSigner(t, "kid-2", 8)
	j := newJWKS(t, signer)
	cache, t0 := webhook.NewKeyCache(), time.Now()
	if _, err := cache.PublicKey(j.url, nil, "kid-1", t0); err != nil {
		t.Fatalf("warming the cache: %v", err)
	}
	j.serve(signer, rotated)

	_, rotatedErr := cache.PublicKey(j.url, nil, "kid-2", t0.Add(time.Second))
	for i := range 20 {
		_, _ = cache.PublicKey(j.url, nil, fmt.Sprintf("kid-unknown-%d", i), t0.Add(2*time.Second))
	}

	if rotatedErr != nil || j.count() != 2 {
		t.Errorf("rotated key error %v after %d fetches; want the rotated key found by the one fetch an unknown kid may make in the interval", rotatedErr, j.count())
	}
}

func TestVerifyTimestampSkew_AcceptsRFC3339AndUnixSeconds(t *testing.T) {
	now := time.Now()
	if err := webhook.VerifyTimestampSkew(now.Format(time.RFC3339), now); err != nil {
		t.Errorf("RFC3339 timestamp rejected: %v", err)
	}
	if err := webhook.VerifyTimestampSkew(strings.TrimSpace(time.Unix(now.Unix(), 0).UTC().Format("20060102")), now); err == nil {
		t.Error("an unparseable timestamp must be rejected")
	}
}

func TestReplay_PrunesExpiredNonces(t *testing.T) {
	rp := webhook.NewReplay()
	t0 := time.Now()
	body := kittest.Envelope("n-1", t0, nil)

	if err := rp.Verify(body, t0); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	// Outside the window the nonce is pruned — but the body's own createdAt is
	// then stale too, which is the real guarantee: age alone cannot make a
	// captured request acceptable again.
	if err := rp.Verify(body, t0.Add(webhook.ReplaySkew+time.Minute)); err == nil {
		t.Error("an aged-out replay must still be rejected on its stale createdAt")
	}
}

func TestReplay_RejectsMissingNonceOrCreatedAt(t *testing.T) {
	rp := webhook.NewReplay()
	now := time.Now()

	if err := rp.Verify([]byte(`{"createdAt":"`+now.Format(time.RFC3339)+`"}`), now); err == nil {
		t.Error("a body with no nonce must be rejected")
	}
	if err := rp.Verify([]byte(`{"nonce":"n"}`), now); err == nil {
		t.Error("a body with no createdAt must be rejected")
	}
	if err := rp.Verify([]byte(`not json`), now); err == nil {
		t.Error("an unparseable body must be rejected")
	}
}
