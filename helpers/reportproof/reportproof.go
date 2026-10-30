// Package reportproof, an optional helper, signs a node report's exact body and checks that signature, so mitstack can tell a node's own report from one sent with a leaked report token.
package reportproof

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
)

// Header is the HTTP header that carries a report's proof.
const Header = "Mitstack-Report-Proof"

// Leeway is how far past its exp, or before its nbf, a proof still verifies, so clocks may drift.
const Leeway = time.Minute

const lifetime = 10 * time.Minute

// TokenRequestScript prints the proof of a body on a Kubernetes node, where the API server signs it and the node holds no key.
//
//go:embed scripts/token-request.sh
var TokenRequestScript string

var errNotSigned = errors.New("the proof is not signed by a key in the recorded set")

// Audience names the body a proof is for: report-sha256: and the lowercase hex SHA-256 of its exact bytes.
func Audience(body []byte) string {
	return fmt.Sprintf("report-sha256:%x", sha256.Sum256(body))
}

// Sign returns the proof of body for platformOrigin, valid for ten minutes from now, for a node that holds its own key.
func Sign(key ed25519.PrivateKey, kid string, body []byte, platformOrigin string, now time.Time) string {
	input := segment(map[string]string{"alg": "EdDSA", "kid": kid, "typ": "JWT"}) + "." +
		segment(map[string]any{"aud": []string{platformOrigin, Audience(body)}, "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(lifetime).Unix()})
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

// KeySet is the key set a node that signs with Sign reports as its jwks member, holding key under kid.
func KeySet(kid string, key ed25519.PublicKey) []byte {
	set, _ := json.Marshal(map[string][]map[string]string{"keys": {{
		"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": kid, "x": base64.RawURLEncoding.EncodeToString(key),
	}}})
	return set
}

// Verify returns nil when a key of keySet signs proof for platformOrigin and body at now, or an error saying why not.
func Verify(proof string, body, keySet []byte, platformOrigin string, now time.Time) error {
	payload, err := verifiedPayload(proof, keySet)
	if err != nil {
		return err
	}
	var claims struct {
		Audience  audience `json:"aud"`
		ExpiresAt *int64   `json:"exp"`
		NotBefore *int64   `json:"nbf"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return errors.New("the proof's claims cannot be read")
	}
	switch {
	case !slices.Contains(claims.Audience, platformOrigin):
		return errors.New("the proof does not name the platform's audience")
	case !slices.Contains(claims.Audience, Audience(body)):
		return errors.New("the proof was minted for another body")
	case claims.ExpiresAt == nil:
		return errors.New("the proof carries no expiry")
	case now.After(time.Unix(*claims.ExpiresAt, 0).Add(Leeway)):
		return errors.New("the proof has expired")
	case claims.NotBefore != nil && now.Before(time.Unix(*claims.NotBefore, 0).Add(-Leeway)):
		return errors.New("the proof is not valid yet")
	case time.Unix(*claims.ExpiresAt, 0).After(now.Add(lifetime + Leeway)):
		return errors.New("the proof expires more than ten minutes from now")
	}
	return nil
}

func verifiedPayload(proof string, keySet []byte) ([]byte, error) {
	parts := strings.Split(proof, ".")
	var header struct{ Alg, Kid string }
	if len(parts) != 3 || decodeSegment(parts[0], &header) != nil {
		return nil, errors.New("the proof is not a compact JWS")
	}
	keys, err := publicKeys(keySet)
	if err != nil {
		return nil, errors.New("the recorded key set cannot be read")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	key, held := keys[header.Kid]
	if err != nil || !held || !verifies(header.Alg, key, []byte(parts[0]+"."+parts[1]), signature) {
		return nil, errNotSigned
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("the proof's claims cannot be read")
	}
	return payload, nil
}

func verifies(alg string, key crypto.PublicKey, input, signature []byte) bool {
	switch key := key.(type) {
	case *rsa.PublicKey:
		digest := sha256.Sum256(input)
		return alg == "RS256" && rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) == nil
	case ed25519.PublicKey:
		return alg == "EdDSA" && ed25519.Verify(key, input, signature)
	}
	return false
}

func publicKeys(keySet []byte) (map[string]crypto.PublicKey, error) {
	var set struct {
		Keys []struct{ Kty, Crv, Kid, N, E, X string } `json:"keys"`
	}
	if err := json.Unmarshal(keySet, &set); err != nil {
		return nil, err
	}
	keys := map[string]crypto.PublicKey{}
	kids := map[string]bool{}
	for _, k := range set.Keys {
		if k.Kid == "" || kids[k.Kid] {
			return nil, errors.New("a kid is empty or repeated")
		}
		kids[k.Kid] = true
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		switch {
		case k.Kty == "RSA" && errN == nil && errE == nil && len(e) > 0 && len(e) <= 4:
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		case k.Kty == "OKP" && k.Crv == "Ed25519" && errX == nil && len(x) == ed25519.PublicKeySize:
			keys[k.Kid] = ed25519.PublicKey(x)
		}
	}
	return keys, nil
}

// audience is a JWT aud claim, one string or an array of them.
type audience []string

func (a *audience) UnmarshalJSON(raw []byte) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		*a = []string{one}
		return nil
	}
	return json.Unmarshal(raw, (*[]string)(a))
}

func segment(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeSegment(s string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
