// Package provenance is the publisher-signed provenance record (MIT-3097): a
// statement of WHERE a block's published image and manifest came from, signed
// by the identity that published them, written beside the image so the
// platform can read it instead of trusting the block's source repository.
//
// # Why a GitHub Actions OIDC token, and not Sigstore keyless
//
// The kit is stdlib-only (see the module's own CLAUDE.md): no third-party
// dependency is ever added, however convenient one looks for a single call
// site. Sigstore keyless signing would add Fulcio's certificate authority and
// Rekor's transparency log as new trust anchors, a cosign binary in every
// block's CI, and a verifier this module cannot hold — there is no stdlib
// Sigstore verifier, so the kit could sign but never check its own claim
// offline.
//
// An RS256 JSON Web Token, by contrast, verifies in a few dozen lines of
// crypto/rsa, crypto/sha256 and encoding/json — all stdlib. GitHub Actions
// already mints one for free to every job that requests it
// (permissions: id-token: write), signed by GitHub's own key and stamping
// claims — repository, sha, workflow_ref — that are GitHub's word, not the
// publisher's, which is the whole point: a block cannot forge which
// repository it ran as. That issuer, token.actions.githubusercontent.com, is
// also the one platform/deploy/tofu/block_publish.tf already trusts, with
// subject repo:mitstack-eu/<block>:*, to assume the very role that pushes
// this image. Signing with it adds no new trust boundary, only a new claim
// riding one the publish path already depends on.
//
// Be honest about what this is not: there is no GitHub OIDC JWT verification
// in platform Go today. This package is a NEW verification path in the
// platform's trust surface, not a reuse of an existing one — just the
// smallest new path the kit's stdlib-only rule permits.
//
// # Binding the signature to these exact bytes
//
// GitHub signs whatever audience the caller requests; it does not sign the
// record itself. So the record's payload is canonically encoded, sha256'd,
// and that digest becomes the requested audience:
// "mitstack-provenance:sha256:<hex>". Verifying the token's aud claim against
// a freshly recomputed Audience(payload) is therefore what ties GitHub's
// signature to these precise bytes — change one byte of the payload and the
// audience no longer matches, and Verify reports ErrRecordTampered.
//
// # Why exp is deliberately not checked, and the consequence
//
// A record attests a publish that already happened; an OIDC token lives
// about ten minutes. Checking exp would make every record unverifiable
// within the hour it was written, which defeats the point of writing it at
// all. Verify does check that iat is not more than five minutes in the
// future, so a record cannot claim to come from a publish that has not
// happened yet, but the token's own expiry is ignored entirely.
//
// The consequence: GitHub periodically rotates its OIDC signing keys, so a
// record's kid eventually ages out of the published JWKS and the record
// becomes unverifiable from that point on — not because anything about the
// record changed, but because the key that once vouched for it is gone. The
// platform is expected to verify a record once, at pin time, and persist the
// verdict, rather than re-verify it indefinitely against a JWKS that will not
// hold the same keys forever.
//
// # Why the pushed record is a tagged artifact with no OCI subject
//
// platform/deploy/tofu/operators.tf expires manifests with tagStatus:
// untagged after seven days, and tagged operator images are never touched by
// that policy. An untagged OCI referrer — the natural shape for "a manifest
// ABOUT another manifest" — would therefore be garbage-collected a week
// after the very publish it records. platform/scripts/build-push-cached.sh
// already disables buildx's own provenance and SBOM attestations for the
// related reason that referrers accumulate against ECR's referrer cap.
//
// So blockprov push writes the record as a manifest tagged
// sha256-<hex-of-image-digest>.prov, derived from the image digest the
// platform already holds, and sets no subject descriptor: the reader
// computes the tag itself and never calls the referrers API or needs the
// extra ECR permission that API implies. artifactType stays on the manifest
// so the media type alone still says what it is.
//
// # Distinct from package attest
//
// Package attest proves a block's BEHAVIOUR passed the conformance contract,
// signed by the platform's own key. This package proves where the published
// image and its manifest came from, signed by the publisher's identity.
// Neither substitutes for the other: a block can pass every conformance
// suite from an image nobody can trace, and an image can be traced to its
// exact publisher while its behaviour was never checked. attest.go draws the
// same line from its own side.
package provenance
