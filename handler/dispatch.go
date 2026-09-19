package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/events"
	"github.com/mitstack-eu/operator-kit/safe"
)

// dispatchRequest is the second half of the POST /hook pipeline: everything
// after the trust boundary and envelope decode have already succeeded. It
// stashes the events envelope on ctx, replays a cached idempotent response
// without touching the Resource, and otherwise emits progress/completed/
// failed around the verb dispatch (ADR 0042).
func (h *Handler) dispatchRequest(w http.ResponseWriter, r *http.Request, req contract.Request) {
	secrets := secretsFor(req)

	var callback *events.Callback
	if req.EventsCallback != nil {
		callback = &events.Callback{URL: req.EventsCallback.URL, Token: req.EventsCallback.Token}
	}
	ctx := events.WithEnvelope(r.Context(), events.Envelope{
		SubscriptionID: req.SubscriptionID,
		Operation:      req.Operation,
		IdempotencyKey: req.IdempotencyKey,
		Callback:       callback,
	})

	key := idempotencyKey{subscriptionID: req.SubscriptionID, idempotencyKey: req.IdempotencyKey}
	if req.IdempotencyKey != "" {
		if entry, ok := h.idempotency.get(h.now(), key); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(entry.status)
			_, _ = w.Write(entry.body)
			return
		}
	}

	_ = events.EmitProgress(ctx, req.Operation+" starting")

	status, resp := h.dispatchVerb(ctx, req, secrets)

	if status >= 200 && status < 300 {
		_ = events.EmitCompleted(ctx)
	} else {
		detail := resp.Error
		if detail == "" {
			detail = resp.StatusDetail
		}
		_ = events.EmitFailed(ctx, detail)
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "handler: encoding response failed: "+err.Error())
		return
	}

	if req.IdempotencyKey != "" {
		h.idempotency.put(h.now(), key, status, raw)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// dispatchVerb calls the Resource and its declared hooks for req.Operation.
// contract.DecodeRequest has already validated the operation enum, so the
// four cases here are exhaustive.
func (h *Handler) dispatchVerb(ctx context.Context, req contract.Request, secrets []string) (int, contract.Response) {
	desired := req.Desired()
	id := req.Identity()

	switch req.Operation {
	case "subscribe", "upgrade":
		return h.ensureAndSettle(ctx, desired, id, secrets)
	case "validate":
		return h.validate(ctx, id, secrets)
	case "unsubscribe":
		return h.deleteAndSettle(ctx, id, secrets)
	default:
		return http.StatusInternalServerError, contract.Response{Error: "handler: unreachable — unknown operation " + req.Operation}
	}
}

// ensureAndSettle runs beforeEnsure, Ensure, afterEnsure, then polls Observe
// until Present or the verb budget runs out. subscribe and upgrade share
// this exact call sequence.
func (h *Handler) ensureAndSettle(ctx context.Context, d contract.Desired, id contract.Identity, secrets []string) (int, contract.Response) {
	if hook, ok := h.hooks[HookBeforeEnsure]; ok {
		if err := hook(ctx, d); err != nil {
			return h.errorResponse(err, secrets)
		}
	}
	if err := h.resource.Ensure(ctx, d); err != nil {
		return h.errorResponse(err, secrets)
	}
	if hook, ok := h.hooks[HookAfterEnsure]; ok {
		if err := hook(ctx, d); err != nil {
			return h.errorResponse(err, secrets)
		}
	}
	return h.settlePresent(ctx, id, secrets)
}

// deleteAndSettle runs beforeDelete, Delete, afterDelete, then polls Observe
// until Absent or the verb budget runs out.
func (h *Handler) deleteAndSettle(ctx context.Context, id contract.Identity, secrets []string) (int, contract.Response) {
	desired := contract.Desired{Identity: id}

	if hook, ok := h.hooks[HookBeforeDelete]; ok {
		if err := hook(ctx, desired); err != nil {
			return h.errorResponse(err, secrets)
		}
	}
	if err := h.resource.Delete(ctx, id); err != nil {
		return h.errorResponse(err, secrets)
	}
	if hook, ok := h.hooks[HookAfterDelete]; ok {
		if err := hook(ctx, desired); err != nil {
			return h.errorResponse(err, secrets)
		}
	}
	return h.settleAbsent(ctx, id, secrets)
}

// settlePresent polls Observe up to VerbBudget times, stopping early on
// Present. Present within budget answers 200 with the block's own
// Observation.Response(). Budget exhaustion while Absent or Unknown answers
// 200 {"pending":true} — pending is never an error (MIT-1293).
func (h *Handler) settlePresent(ctx context.Context, id contract.Identity, secrets []string) (int, contract.Response) {
	var last contract.Observation
	for attempt := 1; attempt <= h.verbBudget; attempt++ {
		obs, err := h.resource.Observe(ctx, id)
		if err != nil {
			return h.errorResponse(err, secrets)
		}
		last = obs
		if obs.State == contract.Present {
			return http.StatusOK, scrubResponse(obs.Response(), secrets)
		}
		if attempt < h.verbBudget {
			h.sleep(ctx, h.pollInterval)
		}
	}
	return http.StatusOK, contract.Response{
		Pending:      true,
		StatusDetail: safe.Scrub(fmt.Sprintf("still %s after %d attempts", last.State, h.verbBudget), secrets...),
	}
}

// settleAbsent polls Observe up to VerbBudget times, stopping early on
// Absent. Unlike settlePresent (and unlike validate), it always answers a
// definite boolean: Absent within budget is absent:true, and anything else
// — including Unknown — is absent:false with a detail, never an omitted
// marker.
func (h *Handler) settleAbsent(ctx context.Context, id contract.Identity, secrets []string) (int, contract.Response) {
	var last contract.Observation
	for attempt := 1; attempt <= h.verbBudget; attempt++ {
		obs, err := h.resource.Observe(ctx, id)
		if err != nil {
			return h.errorResponse(err, secrets)
		}
		last = obs
		if obs.State == contract.Absent {
			absent := true
			return http.StatusOK, contract.Response{
				Absent:       &absent,
				Outputs:      obs.Outputs,
				StatusDetail: safe.Scrub(obs.StatusDetail, secrets...),
			}
		}
		if attempt < h.verbBudget {
			h.sleep(ctx, h.pollInterval)
		}
	}
	absent := false
	return http.StatusOK, contract.Response{
		Absent:       &absent,
		StatusDetail: safe.Scrub(fmt.Sprintf("observed %s after %d attempts", last.State, h.verbBudget), secrets...),
	}
}

// validate calls Observe exactly once and never polls. It returns
// Observation.Response(), with "pending":true layered on top for Unknown —
// the half Response() cannot carry, since its own doc says it reports what
// was observed rather than that a resource is still converging. An Observe
// error is classified, never folded into pending.
func (h *Handler) validate(ctx context.Context, id contract.Identity, secrets []string) (int, contract.Response) {
	obs, err := h.resource.Observe(ctx, id)
	if err != nil {
		return h.errorResponse(err, secrets)
	}
	resp := obs.Response()
	if obs.State == contract.Unknown {
		resp.Pending = true
	}
	return http.StatusOK, scrubResponse(resp, secrets)
}

// errorResponse classifies err into the wire response the CONTRACT pins: a
// *Refused (by errors.As) is 400 with statusDetail "refused:<class>: <msg>",
// anything else is 500 with no such prefix. A *Refused whose Class is empty
// logs that the block returned a refusal with no class, since a silent
// "unclassified" on the wire would otherwise be the only trace of it.
func (h *Handler) errorResponse(err error, secrets []string) (int, contract.Response) {
	var refused *Refused
	if errors.As(err, &refused) {
		if refused.Class == "" {
			log.Printf("handler: block returned a refusal with no class: %s", safe.LogSafe(refused.Detail, secrets...))
		}
		return http.StatusBadRequest, contract.Response{
			Error:        safe.Scrub(err.Error(), secrets...),
			StatusDetail: safe.Scrub(refused.Error(), secrets...),
		}
	}

	log.Printf("handler: %s", safe.LogSafe(err.Error(), secrets...))
	return http.StatusInternalServerError, contract.Response{Error: safe.Scrub(err.Error(), secrets...)}
}

// scrubResponse redacts every secret value out of the two free-text fields a
// Resource controls, before the response reaches the wire.
func scrubResponse(resp contract.Response, secrets []string) contract.Response {
	resp.Error = safe.Scrub(resp.Error, secrets...)
	resp.StatusDetail = safe.Scrub(resp.StatusDetail, secrets...)
	return resp
}

// secretsFor collects every literal secret value a request carries —
// secrets, secretInputs, appSecrets, the OBO token, the events callback
// token — so the handler can scrub all of them out of anything it writes to
// the response body or a log line.
func secretsFor(req contract.Request) []string {
	var out []string
	for _, group := range req.Secrets {
		for _, v := range group {
			out = append(out, v)
		}
	}
	for _, v := range req.SecretInputs {
		out = append(out, v)
	}
	for _, v := range req.AppSecrets {
		out = append(out, v)
	}
	if req.OBO != "" {
		out = append(out, req.OBO)
	}
	if req.EventsCallback != nil && req.EventsCallback.Token != "" {
		out = append(out, req.EventsCallback.Token)
	}
	return out
}
