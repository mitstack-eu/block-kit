package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/safe"
)

const (
	classWrongCapability  = "wrong-capability"
	classNoConnectionTest = "no-connection-check"
	classUndeclaredVerb   = "undeclared-operation"
)

// ConnectionCheck is the whole of a verify-connection: the capability's slug, the connecting org and the credential the org entered, keyed by the manifest's token field names.
type ConnectionCheck struct {
	CapabilitySlug string
	OrgID          string
	Credential     map[string]string
}

// Verification is the vendor's answer to a credential: a contract.Verdict constant, and in Detail why, in words that carry no part of the credential.
type Verification struct {
	Verdict string
	Detail  string
}

// ConnectionVerifier answers a verify-connection by asking the capability's vendor, through guardedhttp's client, whether the credential works.
type ConnectionVerifier interface {
	VerifyConnection(ctx context.Context, c ConnectionCheck) (Verification, error)
}

// verifyConnection serves the one operation that has no subscription, so it is signed like every call but carries no OBO token, and it is never replayed from the idempotency cache since a credential may have been revoked since.
func (h *Handler) verifyConnection(ctx context.Context, req contract.Request, secrets []string) (int, contract.Response) {
	if h.verifier == nil {
		return h.errorResponse(Refuse(classNoConnectionTest, "this operator's manifest does not list %s", contract.OperationVerifyConnection), secrets)
	}
	if req.CapabilitySlug != h.audience {
		return h.errorResponse(Refuse(classWrongCapability, "this operator checks the credential of capability %q, not %q", h.audience, req.CapabilitySlug), secrets)
	}
	answer, err := h.verifier.VerifyConnection(ctx, ConnectionCheck{CapabilitySlug: req.CapabilitySlug, OrgID: req.OrgID, Credential: req.EnteredCredential})
	if err != nil {
		return h.errorResponse(err, secrets)
	}
	switch answer.Verdict {
	case contract.VerdictVerified, contract.VerdictRejected, contract.VerdictUnreachable:
	default:
		return h.errorResponse(fmt.Errorf("handler: the verifier answered the verdict %q, which is none of %s, %s or %s",
			answer.Verdict, contract.VerdictVerified, contract.VerdictRejected, contract.VerdictUnreachable), secrets)
	}
	return http.StatusOK, contract.Response{Verdict: answer.Verdict, StatusDetail: safe.Scrub(answer.Detail, secrets...)}
}
