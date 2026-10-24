package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/events"
	"github.com/mitstack-eu/block-kit/helpers/safe"
)

// dispatchRequest runs the verb through the idempotency cache, storing only a settled answer.
//
// A pending:true answer, or an unsubscribe answered absent:false, is never replayed so the engine's re-drive observes the resource again. The cache key carries the named resource, and an empty idempotencyKey or a validate skips the cache because a replayed probe would report a state the resource may have left; an issue skips it because a replayed credential may have expired.
func (h *Handler) dispatchRequest(w http.ResponseWriter, r *http.Request, req contract.Request) {
	secrets := h.secretsFor(req)

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

	run := func() (int, []byte, bool) {
		status, resp := h.dispatchVerbRecovering(ctx, req, secrets)
		settled := answerSettled(req.Operation, resp)
		h.emitVerbEvent(ctx, req.Operation, status, resp, settled)

		raw, err := json.Marshal(resp)
		if err != nil {
			raw, _ = json.Marshal(contract.Response{Error: "handler: encoding response failed: " + err.Error()})
			return http.StatusInternalServerError, raw, true
		}
		return status, raw, settled
	}

	var status int
	var raw []byte
	if req.IdempotencyKey != "" && req.Operation != "validate" && req.Operation != contract.OperationIssue {
		key := idempotencyKey{subscriptionID: req.SubscriptionID, idempotencyKey: req.IdempotencyKey, resource: req.Resource}
		status, raw = h.idempotency.do(h.now(), key, run)
	} else {
		status, raw, _ = run()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// answerSettled is the one verdict the idempotency cache and the verb's event both read: an answer is settled unless it is pending or an unsubscribe still observed present.
func answerSettled(operation string, resp contract.Response) bool {
	notYetAbsent := operation == "unsubscribe" && resp.Absent != nil && !*resp.Absent
	return !resp.Pending && !notYetAbsent
}

// dispatchVerbRecovering recovers inside the single-flight call because a recover further out would leave a waiter on the same key holding a zero status, and its fixed error carries nothing from the panic value since the platform stores the response body.
func (h *Handler) dispatchVerbRecovering(ctx context.Context, req contract.Request, secrets []string) (status int, resp contract.Response) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		log.Printf("handler: block %s subscription %s: %s panicked: %s",
			safe.LogSafe(req.BlockID, secrets...),
			safe.LogSafe(req.SubscriptionID, secrets...),
			req.Operation,
			panicValueForLog(p, secrets))
		status = http.StatusInternalServerError
		resp = contract.Response{Error: "handler: the block panicked during " + req.Operation}
	}()
	return h.dispatchVerb(ctx, req, secrets)
}

// panicValueForLog renders any value but a string or an error only as its type, because formatting it could print a secret a literal scrub cannot find, such as a []byte printed as decimal bytes.
func panicValueForLog(p any, secrets []string) string {
	switch v := p.(type) {
	case string:
		return safe.LogSafe(v, secrets...)
	case error:
		return safe.LogSafe(v.Error(), secrets...)
	default:
		return fmt.Sprintf("value of type %T", p)
	}
}

// emitVerbEvent emits none for validate, since ProbeHealth dispatches validate in answer to a terminal event and a completed one would loop, and none for an issue, which changes nothing the subscription's events record.
//
// It emits no start event because the event log's partial unique index on (subscriptionId, idempotencyKey) would drop the terminal event under a start event that took its slot.
func (h *Handler) emitVerbEvent(ctx context.Context, operation string, status int, resp contract.Response, settled bool) {
	if operation == "validate" || operation == contract.OperationIssue {
		return
	}
	if status >= 200 && status < 300 {
		if !settled {
			_ = events.EmitProgress(ctx, resp.StatusDetail)
		} else {
			_ = events.EmitCompleted(ctx)
		}
		return
	}
	detail := resp.Error
	if detail == "" {
		detail = resp.StatusDetail
	}
	_ = events.EmitFailed(ctx, detail)
}

func (h *Handler) dispatchVerb(ctx context.Context, req contract.Request, secrets []string) (int, contract.Response) {
	if req.Operation == contract.OperationIssue {
		return h.issue(ctx, req, secrets)
	}
	return h.dispatchDeclared(ctx, req.Operation, req.Desired(), secrets)
}

// refuseSecretValues is never handed SecretOutputs, where a secret is returned on purpose, and matches exact values so a value merely containing a secret is left alone.
func refuseSecretValues(field string, values map[string]string, secrets map[string]map[string]string) error {
	for key, value := range values {
		for _, group := range secrets {
			for _, secretValue := range group {
				// An empty secret value is an unset optional secret and must not combine with an empty output into a false refusal.
				if secretValue == "" {
					continue
				}
				if value == secretValue {
					return Refuse("secret-in-output", "%s %q carries a secret value", field, key)
				}
			}
		}
	}
	return nil
}

func mergeOutputs(base, overlay map[string]string) map[string]string {
	if len(overlay) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overlay {
		merged[k] = v
	}
	return merged
}

// errorResponse answers a *Refused with 400 and statusDetail "refused:<class>: <msg>" and anything else with 500, and logs a refusal with no class since the wire would otherwise be the only trace.
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

// secretsFor collects every literal secret a request carries so all can be scrubbed from the response body and logs.
func (h *Handler) secretsFor(req contract.Request) []string {
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
