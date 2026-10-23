// Package webhook, part of the core every block needs, implements the lifecycle-webhook trust boundary an operator consumes, verifying mitstack's detached Ed25519 signature against a public key fetched from its JWKS with no shared secret.
package webhook

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ReplaySkew bounds how far a signed request's createdAt may differ from this verifier's clock, closing the replay hole in a detached signature.
const ReplaySkew = 5 * time.Minute

// Replay rejects webhook bodies whose signed createdAt is outside ReplaySkew or whose nonce was already seen inside it.
type Replay struct {
	mu sync.Mutex
	// Points at the caller's map variable, so reassigning that variable resets the guard and a map held by value would not.
	nonces *map[string]time.Time
}

// NewReplay returns an empty replay guard owning its own nonce map.
func NewReplay() *Replay {
	m := map[string]time.Time{}
	return &Replay{nonces: &m}
}

// NewReplayOver returns a replay guard backed by the caller's map variable.
func NewReplayOver(nonces *map[string]time.Time) *Replay { return &Replay{nonces: nonces} }

// Verify rejects a webhook whose createdAt is outside the skew window or whose nonce was already seen, and must run after the signature check because it trusts both fields.
func (rp *Replay) Verify(body []byte, now time.Time) error {
	var env struct {
		CreatedAt time.Time `json:"createdAt"`
		Nonce     string    `json:"nonce"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return err
	}
	if env.Nonce == "" {
		return errors.New("missing nonce")
	}
	if env.CreatedAt.IsZero() {
		return errors.New("missing createdAt")
	}
	skew := now.Sub(env.CreatedAt)
	if skew < 0 {
		skew = -skew
	}
	if skew > ReplaySkew {
		return errors.New("stale request timestamp")
	}
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if rp.nonces == nil {
		m := map[string]time.Time{}
		rp.nonces = &m
	}
	if *rp.nonces == nil {
		*rp.nonces = map[string]time.Time{}
	}
	for n, t := range *rp.nonces {
		if now.Sub(t) > ReplaySkew {
			delete(*rp.nonces, n)
		}
	}
	if _, seen := (*rp.nonces)[env.Nonce]; seen {
		return errors.New("replayed nonce")
	}
	(*rp.nonces)[env.Nonce] = now
	return nil
}

// VerifyTimestampSkew bounds a GET's signed timestamp, in RFC3339 or unix seconds, by ReplaySkew, and must run after the signature check.
func VerifyTimestampSkew(ts string, now time.Time) error {
	if ts == "" {
		return errors.New("missing timestamp")
	}
	var t time.Time
	if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
		t = parsed
	} else if secs, err2 := strconv.ParseInt(ts, 10, 64); err2 == nil {
		t = time.Unix(secs, 0)
	} else {
		return errors.New("unparseable timestamp")
	}
	skew := now.Sub(t)
	if skew < 0 {
		skew = -skew
	}
	if skew > ReplaySkew {
		return errors.New("stale request timestamp")
	}
	return nil
}

// JWKSRefreshInterval bounds how long a key removed from the JWKS still verifies, and how often a caller can make a KeyCache fetch.
const JWKSRefreshInterval = 5 * time.Minute

// KeyCache holds the Ed25519 keys of one JWKS document, and is safe for concurrent use.
type KeyCache struct {
	mu                             sync.Mutex
	keys                           map[string]ed25519.PublicKey
	lastFetch, lastUnknownKidFetch time.Time
}

func NewKeyCache() *KeyCache { return &KeyCache{} }

// PublicKey refetches the JWKS once JWKSRefreshInterval has passed since the last fetch, or for a kid it lacks when no unknown kid caused a fetch within the interval; a failed fetch keeps the last keys and waits out the interval, unless the cache holds none.
func (kc *KeyCache) PublicKey(jwksURL string, client *http.Client, kid string, now time.Time) (ed25519.PublicKey, error) {
	kc.mu.Lock()
	defer kc.mu.Unlock()
	_, known := kc.keys[kid]
	due := now.Sub(kc.lastFetch) >= JWKSRefreshInterval
	unknownKidFetch := !due && !known && now.Sub(kc.lastUnknownKidFetch) >= JWKSRefreshInterval
	if !due && !unknownKidFetch {
		return keyFor(kc.keys, kid)
	}
	keys, err := fetchJWKS(jwksURL, client)
	if err == nil {
		kc.keys = keys
	}
	if err == nil || len(kc.keys) > 0 {
		kc.lastFetch = now
		if unknownKidFetch {
			kc.lastUnknownKidFetch = now
		}
	}
	if err != nil && !known {
		return nil, err
	}
	return keyFor(kc.keys, kid)
}

func keyFor(keys map[string]ed25519.PublicKey, kid string) (ed25519.PublicKey, error) {
	pub, ok := keys[kid]
	if !ok {
		return nil, errors.New("kid not found in jwks")
	}
	return pub, nil
}

func fetchJWKS(jwksURL string, client *http.Client) (map[string]ed25519.PublicKey, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Get(jwksURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("jwks answered HTTP " + strconv.Itoa(resp.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	return keysFromJWKS(raw)
}

// VerifyEd25519Sig strips the "ed25519=" prefix, base64-decodes, and verifies.
func VerifyEd25519Sig(pub ed25519.PublicKey, body []byte, sigHeader string) bool {
	sigHeader = strings.TrimPrefix(sigHeader, "ed25519=")
	sig, err := base64.StdEncoding.DecodeString(sigHeader)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, body, sig)
}

// PublicKeyFromJWKS extracts the Ed25519 (OKP) key for kid from a JWKS document.
func PublicKeyFromJWKS(jwksJSON []byte, kid string) (ed25519.PublicKey, error) {
	keys, err := keysFromJWKS(jwksJSON)
	if err != nil {
		return nil, err
	}
	return keyFor(keys, kid)
}

func keysFromJWKS(jwksJSON []byte) (map[string]ed25519.PublicKey, error) {
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Kid string `json:"kid"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(jwksJSON, &doc); err != nil {
		return nil, err
	}
	keys := make(map[string]ed25519.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		raw, err := base64.RawURLEncoding.DecodeString(k.X)
		if k.Kty != "OKP" || k.Crv != "Ed25519" || err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		keys[k.Kid] = ed25519.PublicKey(raw)
	}
	return keys, nil
}
