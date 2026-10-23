package webhook

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrOBOExpired is returned by VerifyOBO when the token's expiry has passed.
var ErrOBOExpired = errors.New("obo token expired")

// ErrOBOInvalid is returned by VerifyOBO when the token is malformed or badly signed.
var ErrOBOInvalid = errors.New("obo token invalid")

// OBOClaims are the identity claims carried inside an on-behalf-of token.
type OBOClaims struct {
	OrgID          string `json:"org_id"`
	AppID          string `json:"app_id"`
	SubscriptionID string `json:"sub_id"`
	Audience       string `json:"aud,omitempty"`
}

type oboPayload struct {
	OBOClaims
	Kid       string    `json:"kid"`
	ExpiresAt time.Time `json:"exp"`
}

// MintOBO mirrors mitstack's Ed25519 token format for tests, since the operator itself only verifies.
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

// VerifyOBO verifies an Ed25519-signed OBO token against pub, returning ErrOBOInvalid for structural or signature failures and ErrOBOExpired past expiry, and leaves comparing the claims to the request to its caller.
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
	return p.OBOClaims, nil
}
