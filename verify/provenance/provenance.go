package provenance

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// SchemaVersion is the version of the record shape this package reads and writes.
const SchemaVersion = "1.0.0"

// Issuer is the only token issuer a record may carry.
const Issuer = "https://token.actions.githubusercontent.com"

// AudiencePrefix namespaces the content-bound audience requested from GitHub's OIDC token endpoint.
const AudiencePrefix = "mitstack-provenance:"

// ArtifactType is the OCI artifactType a pushed record manifest carries.
const ArtifactType = "application/vnd.mitstack.block-provenance.v1+json"

// Schema is the embedded JSON Schema for a record.
//
//go:embed record.schema.json
var Schema []byte

// GoldensDir is the directory inside Goldens holding the committed record fixtures.
const GoldensDir = "goldens"

// Goldens embeds the committed record fixtures, including the records prefixed InvalidPrefix that Verify must reject.
//
//go:embed goldens/*.json
var Goldens embed.FS

// InvalidPrefix marks a golden file name as belonging to the rejection half of the fixture set.
const InvalidPrefix = "invalid-"

const jwksSuffix = ".jwks.json"

var (
	// ErrNotConformant is returned by Build for anything but a full pass, and by Verify for a record whose verdict is not "pass".
	ErrNotConformant = errors.New("provenance: conformance verdict is not \"pass\"")
	// ErrIncomplete is returned when a required payload field is empty.
	ErrIncomplete = errors.New("provenance: a required payload field is empty")
	// ErrMalformedToken is returned for a token that is not three base64url segments, has an undecodable header, an alg other than RS256, an empty kid, or an iat more than five minutes ahead.
	ErrMalformedToken = errors.New("provenance: token is malformed")
	// ErrUnknownKid is returned when the resolver has no key for the token's kid.
	ErrUnknownKid = errors.New("provenance: unknown signing key id")
	// ErrBadSignature is returned when the RS256 signature does not verify.
	ErrBadSignature = errors.New("provenance: signature does not verify")
	// ErrUntrustedIssuer is returned when the token's iss claim is not Issuer.
	ErrUntrustedIssuer = errors.New("provenance: untrusted token issuer")
	// ErrRecordTampered is returned when the token's aud claim does not equal Audience(payload).
	ErrRecordTampered = errors.New("provenance: audience does not match the payload — the record has been tampered with")
	// ErrWrongIdentity is returned when the token's repository or sha claim differs from the expected or payload value, or no expected repository was supplied.
	ErrWrongIdentity = errors.New("provenance: token identity does not match the expected repository")
	// ErrManifestChanged is returned when manifestSha256 does not match the sha256 of the decoded manifest bytes.
	ErrManifestChanged = errors.New("provenance: manifest sha256 does not match the decoded manifest bytes")
	// ErrUnknownField is returned by Decode for a field the record's shape does not declare.
	ErrUnknownField = errors.New("provenance: record contains a field its shape does not declare")
)

// Conformance is the block-verify result a record attests.
type Conformance struct {
	SuiteVersion  string `json:"suiteVersion"`
	Verdict       string `json:"verdict"`
	ResultsDigest string `json:"resultsDigest"`
	RunURL        string `json:"runUrl"`
}

// Payload is everything the record asserts, and the audience is computed over its canonical encoding so changing any byte invalidates the token.
type Payload struct {
	SchemaVersion    string      `json:"schemaVersion"`
	BlockID          string      `json:"blockId"`
	BlockVersion     string      `json:"blockVersion"`
	ImageRef         string      `json:"imageRef"`
	ImageDigest      string      `json:"imageDigest"`
	Manifest         string      `json:"manifest"`       // base64 std encoding of manifest.yaml's raw bytes
	ManifestSha256   string      `json:"manifestSha256"` // "sha256:<hex>" over those same raw bytes
	KitVersion       string      `json:"kitVersion"`     // the kit version the block's go.mod requires
	Conformance      Conformance `json:"conformance"`
	SourceRepository string      `json:"sourceRepository"`
	SourceCommit     string      `json:"sourceCommit"`
	IssuedAt         time.Time   `json:"issuedAt"`
}

// Record is the signed record. Token IS the signature.
type Record struct {
	Payload Payload `json:"payload"`
	Token   string  `json:"token"`
}

// Decode refuses any undeclared field, because a plain json.Unmarshal drops it and the audience still matches, so every caller must use Decode.
func Decode(raw []byte) (Record, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var rec Record
	if err := dec.Decode(&rec); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return Record{}, fmt.Errorf("%w: %v", ErrUnknownField, err)
		}
		return Record{}, fmt.Errorf("provenance: decoding record: %w", err)
	}
	return rec, nil
}

// Canonical encodes a payload deterministically, relying on Go's stable struct field order for both writer and reader.
func Canonical(p Payload) ([]byte, error) {
	return json.Marshal(p)
}

// Audience returns the content-bound audience the publishing workflow requests and Verify recomputes.
func Audience(p Payload) (string, error) {
	canon, err := Canonical(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return AudiencePrefix + "sha256:" + hex.EncodeToString(sum[:]), nil
}

func requiredFieldsPresent(p Payload) error {
	switch {
	case p.SchemaVersion == "":
		return fmt.Errorf("%w: schemaVersion", ErrIncomplete)
	case p.BlockID == "":
		return fmt.Errorf("%w: blockId", ErrIncomplete)
	case p.BlockVersion == "":
		return fmt.Errorf("%w: blockVersion", ErrIncomplete)
	case p.ImageRef == "":
		return fmt.Errorf("%w: imageRef", ErrIncomplete)
	case p.ImageDigest == "":
		return fmt.Errorf("%w: imageDigest", ErrIncomplete)
	case p.Manifest == "":
		return fmt.Errorf("%w: manifest", ErrIncomplete)
	case p.ManifestSha256 == "":
		return fmt.Errorf("%w: manifestSha256", ErrIncomplete)
	case p.KitVersion == "":
		return fmt.Errorf("%w: kitVersion", ErrIncomplete)
	case p.SourceRepository == "":
		return fmt.Errorf("%w: sourceRepository", ErrIncomplete)
	case p.SourceCommit == "":
		return fmt.Errorf("%w: sourceCommit", ErrIncomplete)
	}
	return nil
}

// KeyResolver returns the RSA public key for a kid.
type KeyResolver func(kid string) (*rsa.PublicKey, bool)

// Expect is what the verifier requires of the record's identity.
type Expect struct {
	Repository string // required; the repository the caller expects published this
}

// Build assembles a record and refuses a verdict other than "pass" or an incomplete payload, so a failed run writes no record.
func Build(p Payload, mintToken func(audience string) (string, error)) (Record, error) {
	if err := requiredFieldsPresent(p); err != nil {
		return Record{}, err
	}
	if p.Conformance.Verdict != "pass" {
		return Record{}, fmt.Errorf("%w (verdict was %q)", ErrNotConformant, p.Conformance.Verdict)
	}
	aud, err := Audience(p)
	if err != nil {
		return Record{}, err
	}
	token, err := mintToken(aud)
	if err != nil {
		return Record{}, fmt.Errorf("provenance: minting token: %w", err)
	}
	return Record{Payload: p, Token: token}, nil
}

func parseToken(token string) (header, claims map[string]interface{}, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, nil, fmt.Errorf("%w: token is not three segments", ErrMalformedToken)
	}

	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: header is not base64url", ErrMalformedToken)
	}
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return nil, nil, fmt.Errorf("%w: header is not JSON", ErrMalformedToken)
	}
	if alg, _ := header["alg"].(string); alg != "RS256" {
		return nil, nil, fmt.Errorf("%w: alg is %q, want RS256", ErrMalformedToken, header["alg"])
	}
	kid, _ := header["kid"].(string)
	if kid == "" {
		return nil, nil, fmt.Errorf("%w: kid is empty", ErrMalformedToken)
	}

	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: claims are not base64url", ErrMalformedToken)
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		return nil, nil, fmt.Errorf("%w: claims are not JSON", ErrMalformedToken)
	}

	if iat, ok := claims["iat"]; ok {
		f, ok := iat.(float64)
		if !ok {
			return nil, nil, fmt.Errorf("%w: iat is not a number", ErrMalformedToken)
		}
		if time.Unix(int64(f), 0).After(time.Now().UTC().Add(5 * time.Minute)) {
			return nil, nil, fmt.Errorf("%w: iat is more than five minutes in the future", ErrMalformedToken)
		}
	}

	return header, claims, nil
}

func verifySignature(token string, pub *rsa.PublicKey) error {
	parts := strings.Split(token, ".")
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("%w: signature is not base64url", ErrBadSignature)
	}
	hashed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], sig); err != nil {
		return ErrBadSignature
	}
	return nil
}

// Verify fails closed, and judges the presenter-controlled repository and commit claims only after the signature is valid so a forged token leaks nothing about them.
func Verify(r Record, resolve KeyResolver, exp Expect) error {
	header, claims, err := parseToken(r.Token)
	if err != nil {
		return err
	}

	kid, _ := header["kid"].(string)
	pub, ok := resolve(kid)
	if !ok {
		return fmt.Errorf("%w: %q is not in the JWKS", ErrUnknownKid, kid)
	}

	if err := verifySignature(r.Token, pub); err != nil {
		return err
	}

	if iss, _ := claims["iss"].(string); iss != Issuer {
		return fmt.Errorf("%w: %q", ErrUntrustedIssuer, claims["iss"])
	}

	wantAud, err := Audience(r.Payload)
	if err != nil {
		return err
	}
	if aud, _ := claims["aud"].(string); aud != wantAud {
		return ErrRecordTampered
	}

	if exp.Repository == "" {
		return fmt.Errorf("%w: no expected repository was supplied", ErrWrongIdentity)
	}
	repoClaim, _ := claims["repository"].(string)
	shaClaim, _ := claims["sha"].(string)
	switch {
	case repoClaim != exp.Repository:
		return fmt.Errorf("%w: token repository %q, want %q", ErrWrongIdentity, repoClaim, exp.Repository)
	case repoClaim != r.Payload.SourceRepository:
		return fmt.Errorf("%w: token repository %q, payload sourceRepository %q", ErrWrongIdentity, repoClaim, r.Payload.SourceRepository)
	case shaClaim != r.Payload.SourceCommit:
		return fmt.Errorf("%w: token sha %q, payload sourceCommit %q", ErrWrongIdentity, shaClaim, r.Payload.SourceCommit)
	}

	if err := requiredFieldsPresent(r.Payload); err != nil {
		return err
	}

	raw, err := base64.StdEncoding.DecodeString(r.Payload.Manifest)
	if err != nil {
		return fmt.Errorf("%w: manifest is not valid base64", ErrManifestChanged)
	}
	sum := sha256.Sum256(raw)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != r.Payload.ManifestSha256 {
		return fmt.Errorf("%w (recorded %q, computed %q)", ErrManifestChanged, r.Payload.ManifestSha256, got)
	}

	if r.Payload.Conformance.Verdict != "pass" {
		return fmt.Errorf("%w (verdict was %q)", ErrNotConformant, r.Payload.Conformance.Verdict)
	}
	return nil
}

type jwksDocument struct {
	Keys []jwksKey `json:"keys"`
}

type jwksKey struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// ParseJWKS parses a GitHub OIDC JWKS document into a KeyResolver.
func ParseJWKS(raw []byte) (KeyResolver, error) {
	var doc jwksDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("provenance: parsing JWKS: %w", err)
	}

	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("provenance: JWKS key %q has an invalid modulus: %w", k.Kid, err)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("provenance: JWKS key %q has an invalid exponent: %w", k.Kid, err)
		}
		e := 0
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}
	}

	return func(kid string) (*rsa.PublicKey, bool) {
		pub, ok := keys[kid]
		return pub, ok
	}, nil
}

// splitGoldens skips every file ending jwksSuffix by rule, so a second JWKS fixture is excluded the same way.
func splitGoldens() (valid, invalid map[string][]byte, err error) {
	entries, err := Goldens.ReadDir(GoldensDir)
	if err != nil {
		return nil, nil, fmt.Errorf("provenance: reading %s: %w", GoldensDir, err)
	}

	valid = map[string][]byte{}
	invalid = map[string][]byte{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		if strings.HasSuffix(name, jwksSuffix) {
			continue
		}
		raw, err := Goldens.ReadFile(GoldensDir + "/" + name)
		if err != nil {
			return nil, nil, fmt.Errorf("provenance: reading %s/%s: %w", GoldensDir, name, err)
		}
		key := strings.TrimSuffix(name, ".json")
		if strings.HasPrefix(key, InvalidPrefix) {
			invalid[key] = raw
		} else {
			valid[key] = raw
		}
	}
	return valid, invalid, nil
}

// ValidGoldens returns the goldens Verify must accept and errors on an empty set, since a vacuous acceptance set proves nothing.
func ValidGoldens() (map[string][]byte, error) {
	valid, _, err := splitGoldens()
	if err != nil {
		return nil, err
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("provenance: no valid goldens found under %s", GoldensDir)
	}
	return valid, nil
}

// InvalidGoldens returns the goldens Verify must reject and errors on an empty set, since a vacuous rejection set proves nothing.
func InvalidGoldens() (map[string][]byte, error) {
	_, invalid, err := splitGoldens()
	if err != nil {
		return nil, err
	}
	if len(invalid) == 0 {
		return nil, fmt.Errorf("provenance: no invalid goldens found under %s", GoldensDir)
	}
	return invalid, nil
}
