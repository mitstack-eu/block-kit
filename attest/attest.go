// Package attest is the signed conformance attestation (MIT-1474): proof that a
// block passed block-verify, in a form anyone can check without trusting
// whoever is showing it to them.
//
// A verdict nobody can verify is a claim. The requirement is that a block which
// passes the mandatory delivery flow carries a SIGNATURE — something a
// consumer, a catalog, or a third party can check independently.
//
// It reuses the machinery mitstack already has rather than inventing a second
// trust story: the platform Ed25519 signing identity, published at
// /.well-known/jwks.json, which every operator already verifies against. An
// attestation signed with that key is checkable by anyone already integrated
// with the platform — no new trust anchor, no shared secret, no new key
// management. Verification lives HERE, in the kit, precisely so it can be done
// offline against a fetched JWKS.
//
// This is DISTINCT from MIT-394's cosign/SBOM artifact provenance. That answers
// "where did this binary come from". This answers "did this block's BEHAVIOUR
// pass the contract". Both are wanted; neither substitutes for the other.
//
// Three rules, each with a failure mode that motivated it:
//
//   - A PARTIAL PASS IS NEVER SIGNED. There is no "signed with exceptions",
//     because a caveated signature is one nobody reads.
//   - A MANIFEST DIGEST MISMATCH IS INVALID, not stale-but-acceptable. The
//     attestation says something about the block that was verified; a different
//     manifest is a different block.
//   - VERIFICATION FAILS CLOSED on an unknown kid, a bad signature, or a digest
//     mismatch. "We could not check it" must never read as "it is fine".
package attest

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Claims are what an attestation asserts. Every field is part of the signed
// payload; changing any of them invalidates the signature.
type Claims struct {
	// BlockID and Version identify what was verified.
	BlockID string `json:"blockId"`
	Version string `json:"version"`
	// ManifestDigest binds the attestation to the exact manifest. An edited
	// manifest is a different block, and makes this attestation INVALID.
	ManifestDigest string `json:"manifestDigest"`
	// ImageDigest is the operator image actually exercised.
	ImageDigest string `json:"imageDigest,omitempty"`
	// SuiteVersion and KitVersion record what it was verified AGAINST — a pass
	// against an old suite is not the same fact as a pass against the current
	// one.
	SuiteVersion string `json:"suiteVersion"`
	KitVersion   string `json:"kitVersion"`
	// ResultsDigest is a digest of the per-suite results, so the report the
	// verdict came from cannot be swapped after signing.
	ResultsDigest string `json:"resultsDigest"`
	// IssuedAt is when it was signed.
	IssuedAt time.Time `json:"issuedAt"`
	// Kid identifies the signing key, resolved against the JWKS at verify time.
	Kid string `json:"kid"`
}

// Attestation is the signed statement: the claims plus a detached signature
// over their canonical encoding.
type Attestation struct {
	Claims    Claims `json:"claims"`
	Signature string `json:"signature"`
}

var (
	// ErrPartialPass is returned by Sign when asked to attest anything short of
	// a full pass.
	ErrPartialPass = errors.New("attest: refusing to sign — a partial pass is not a pass, and a caveated signature is one nobody reads")
	// ErrUnknownKid is returned when the JWKS has no key for the attestation's kid.
	ErrUnknownKid = errors.New("attest: unknown signing key id")
	// ErrBadSignature is returned when the signature does not verify.
	ErrBadSignature = errors.New("attest: signature does not verify")
	// ErrManifestChanged is returned when the manifest digest no longer matches.
	ErrManifestChanged = errors.New("attest: manifest digest does not match — this attestation describes a different block")
	// ErrIncomplete is returned when required claims are missing.
	ErrIncomplete = errors.New("attest: incomplete claims")
)

// canonical encodes the claims deterministically. Signing and verifying must
// agree byte for byte, so this is the ONE encoder — a struct marshalled with
// Go's default field order is stable, but relying on that implicitly is how a
// signature scheme breaks on a field reorder.
func canonical(c Claims) ([]byte, error) {
	return json.Marshal(c)
}

// Sign produces an attestation for a FULL pass.
//
// verdict is the block-verify verdict; anything other than "pass" is refused.
// The refusal is the point: this is where "mandatory" stops being a word in a
// document and becomes a thing that will not happen.
func Sign(priv ed25519.PrivateKey, kid string, c Claims, verdict string) (Attestation, error) {
	if verdict != "pass" {
		return Attestation{}, fmt.Errorf("%w (verdict was %q)", ErrPartialPass, verdict)
	}
	if c.BlockID == "" || c.Version == "" || c.ManifestDigest == "" || c.SuiteVersion == "" || c.ResultsDigest == "" {
		return Attestation{}, fmt.Errorf("%w: blockId, version, manifestDigest, suiteVersion and resultsDigest are all required", ErrIncomplete)
	}

	c.Kid = kid
	if c.IssuedAt.IsZero() {
		c.IssuedAt = time.Now().UTC()
	}
	payload, err := canonical(c)
	if err != nil {
		return Attestation{}, err
	}
	return Attestation{
		Claims:    c,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)),
	}, nil
}

// KeyResolver returns the public key for a kid — typically backed by a fetched
// JWKS document, so verification needs no call to mitstack.
type KeyResolver func(kid string) (ed25519.PublicKey, bool)

// Verify checks an attestation and confirms it describes the manifest in hand.
//
// It FAILS CLOSED on every uncertainty. "We could not check it" must never be
// reported the same way as "it is fine", which is the reading that makes a
// signature decorative.
func Verify(a Attestation, resolve KeyResolver, manifestDigest string) error {
	if a.Claims.Kid == "" {
		return fmt.Errorf("%w: attestation carries no kid", ErrUnknownKid)
	}
	pub, ok := resolve(a.Claims.Kid)
	if !ok {
		return fmt.Errorf("%w: %q is not in the JWKS", ErrUnknownKid, a.Claims.Kid)
	}

	sig, err := base64.StdEncoding.DecodeString(a.Signature)
	if err != nil {
		return fmt.Errorf("%w: signature is not base64", ErrBadSignature)
	}
	payload, err := canonical(a.Claims)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return ErrBadSignature
	}

	// The digest check is deliberately AFTER the signature check: an attacker
	// controls the digest they present, so comparing first would leak whether a
	// forged signature was otherwise well-formed.
	if manifestDigest != "" && a.Claims.ManifestDigest != manifestDigest {
		return fmt.Errorf("%w (attested %q, in hand %q)", ErrManifestChanged, a.Claims.ManifestDigest, manifestDigest)
	}
	return nil
}

// Status is what a catalog shows for a block.
type Status string

const (
	// Unverified — no attestation. The zero value, because absence of proof is
	// where everything starts and must never be mistaken for proof.
	Unverified Status = "unverified"
	// Verified — a valid attestation matching the manifest in hand.
	Verified Status = "verified"
	// Invalid — an attestation EXISTS but does not check out. This is its own
	// state on purpose: collapsing it into "unverified" would hide the
	// difference between a block nobody verified and a block whose proof stopped
	// matching, and the second is a much louder signal.
	Invalid Status = "invalid"
)

// Evaluate resolves the catalog status for a block.
func Evaluate(a *Attestation, resolve KeyResolver, manifestDigest string) (Status, error) {
	if a == nil {
		return Unverified, nil
	}
	if err := Verify(*a, resolve, manifestDigest); err != nil {
		return Invalid, err
	}
	return Verified, nil
}
