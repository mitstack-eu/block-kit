package webhook

// OBO (on-behalf-of) token verification for a block operator.
// This mirrors the format in api/internal/lifecycle/obo.go — the kit is a
// separate Go module and imports no api packages, so the token format is
// reproduced here using only stdlib.
//
// Format: base64url(json(payload)).base64url(rawEd25519Sig)
//
//	payload = { org_id, app_id, sub_id, kid, exp }
//	sig     = Ed25519 sign over the base64url(payload) segment
//
// The token is signed by mitstack's platform Ed25519 key; the operator verifies
// it with the public key it fetched from mitstack's JWKS — no shared secret.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// ErrOBOExpired is returned by VerifyOBO when the token's expiry has passed.
var ErrOBOExpired = errors.New("obo token expired")

// ErrOBOInvalid is returned by VerifyOBO when the token is malformed, has a
// bad signature, or was signed with the wrong key.
var ErrOBOInvalid = errors.New("obo token invalid")

// OBOClaims are the identity claims carried inside an on-behalf-of token.
type OBOClaims struct {
	OrgID          string `json:"org_id"`
	AppID          string `json:"app_id"`
	SubscriptionID string `json:"sub_id"`
	// Audience binds the token to one operator/block (the block id). When the
	// operator sets MITSTACK_OBO_AUDIENCE, VerifyOBO rejects a token whose aud
	// doesn't match — so a token minted for another operator can't be presented
	// here (T-358). Empty env = not enforced (additive/backward-compatible).
	Audience string `json:"aud,omitempty"`
}

// oboPayload is the full JSON payload marshalled into the token.
type oboPayload struct {
	OBOClaims
	Kid       string    `json:"kid"`
	ExpiresAt time.Time `json:"exp"`
}

// MintOBO mirrors mitstack's Ed25519 token format. Provided for tests that
// simulate mitstack; the operator itself only verifies.
func MintOBO(priv ed25519.PrivateKey, kid string, claims OBOClaims, ttl time.Duration, now time.Time) (string, error) {
	p := oboPayload{OBOClaims: claims, Kid: kid, ExpiresAt: now.Add(ttl)}
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	payloadEnc := base64.RawURLEncoding.EncodeToString(payloadJSON)
	sig := ed25519.Sign(priv, []byte(payloadEnc))
	return payloadEnc + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifyOBO verifies an Ed25519-signed OBO token against pub (the platform
// public key from mitstack's JWKS). Returns ErrOBOInvalid for structural or
// signature failures and ErrOBOExpired when past expiry.
func VerifyOBO(pub ed25519.PublicKey, token string, now time.Time) (OBOClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return OBOClaims{}, ErrOBOInvalid
	}
	payloadEnc, sigEnc := parts[0], parts[1]

	rawSig, err := base64.RawURLEncoding.DecodeString(sigEnc)
	if err != nil || len(rawSig) != ed25519.SignatureSize {
		return OBOClaims{}, ErrOBOInvalid
	}
	if !ed25519.Verify(pub, []byte(payloadEnc), rawSig) {
		return OBOClaims{}, ErrOBOInvalid
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(payloadEnc)
	if err != nil {
		return OBOClaims{}, ErrOBOInvalid
	}
	var p oboPayload
	if err := json.Unmarshal(payloadJSON, &p); err != nil {
		return OBOClaims{}, ErrOBOInvalid
	}
	if now.After(p.ExpiresAt) {
		return OBOClaims{}, ErrOBOExpired
	}
	// Per-operator audience binding (T-358): if this operator declares its
	// expected audience, reject a token minted for a different operator.
	if aud := os.Getenv("MITSTACK_OBO_AUDIENCE"); aud != "" && p.OBOClaims.Audience != aud {
		return OBOClaims{}, ErrOBOInvalid
	}
	return p.OBOClaims, nil
}
