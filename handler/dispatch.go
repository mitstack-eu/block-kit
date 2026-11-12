package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/events"
	"github.com/mitstack-eu/block-kit/helpers/reqcache"
	"github.com/mitstack-eu/block-kit/helpers/safe"
)

// dispatchRequest runs the verb through the idempotency cache, storing only a settled answer.
//
// A pending:true answer, or an unsubscribe answered absent:false, is never replayed so the engine's re-drive observes the resource again. The cache key carries the named resource and unit, and an empty idempotencyKey or a validate skips the cache because a replayed probe would report a state the resource may have left; an issue or a lease skips it because a replayed credential may have expired, and a run skips it because each send reads the host's newest report.
func (h *Handler) dispatchRequest(w http.ResponseWriter, r *http.Request, req contract.Request) {
	secrets := req.SecretValues().All()
	ctx := requestContext(r.Context(), req)

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
	if req.IdempotencyKey != "" && !answersFresh(req.Operation) {
		key := idempotencyKey{subscriptionID: req.SubscriptionID, idempotencyKey: req.IdempotencyKey, resource: req.Resource, unit: req.Unit}
		status, raw = h.idempotency.do(h.now(), key, run)
	} else {
		status, raw, _ = run()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// answersFresh is true of the verbs whose answer is never replayed from the idempotency cache and which emit no event: a validate probes what the resource is now, an issue, a verify-connection and a lease answer a credential or verdict that may have lapsed, and a run answers what the host last reported, which the platform records itself.
func answersFresh(operation string) bool {
	switch operation {
	case "validate", contract.OperationIssue, contract.OperationVerifyConnection, contract.OperationLease, contract.OperationRun:
		return true
	}
	return false
}

func requestContext(ctx context.Context, req contract.Request) context.Context {
	var callback *events.Callback
	if req.EventsCallback != nil {
		callback = &events.Callback{URL: req.EventsCallback.URL, Token: req.EventsCallback.Token}
	}
	return events.WithEnvelope(reqcache.With(ctx), events.Envelope{
		SubscriptionID: req.SubscriptionID,
		Operation:      req.Operation,
		IdempotencyKey: req.IdempotencyKey,
		Callback:       callback,
	})
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

// emitVerbEvent emits none for validate, since ProbeHealth dispatches validate in answer to a terminal event and a completed one would loop, nor for an issue, verify-connection, lease or run, which change nothing the subscription's events record.
//
// It emits no start event because the event log's partial unique index on (subscriptionId, idempotencyKey) would drop the terminal event under a start event that took its slot.
func (h *Handler) emitVerbEvent(ctx context.Context, operation string, status int, resp contract.Response, settled bool) {
	if answersFresh(operation) {
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
	switch {
	case req.Operation == contract.OperationVerifyConnection:
		return h.verifyConnection(ctx, req, secrets)
	case h.resource == nil:
		return h.errorResponse(Refuse(classUndeclaredVerb, "this operator answers %s alone, not %s", contract.OperationVerifyConnection, req.Operation), secrets)
	case req.Operation == contract.OperationIssue:
		return h.issue(ctx, req, secrets)
	case req.Operation == contract.OperationLease:
		return h.lease(ctx, req, secrets)
	case req.Operation == contract.OperationRun:
		return h.run(ctx, req, secrets)
	case req.Operation == contract.OperationSnapshot:
		return h.snapshot(ctx, req, secrets)
	case req.Operation == contract.OperationRestore:
		return h.restore(ctx, req, secrets)
	}
	return h.dispatchDeclared(ctx, req.Operation, req.Desired(), secrets)
}

// refuseSecretValues is never handed SecretOutputs, where a secret is returned on purpose, and matches exact values so a value merely containing a secret is left alone.
func refuseSecretValues(field string, values map[string]string, secrets []string) error {
	for key, value := range values {
		if slices.Contains(secrets, value) {
			return Refuse("secret-in-output", "%s %q carries a secret value", field, key)
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
			Error:        scrubAroundClass(err.Error(), refused, secrets),
			StatusDetail: scrubAroundClass(refused.Error(), refused, secrets),
		}
	}

	log.Printf("handler: %s", safe.LogSafe(err.Error(), secrets...))
	return http.StatusInternalServerError, contract.Response{Error: safe.Scrub(err.Error(), secrets...)}
}

// scrubAroundClass scrubs text everywhere but in refused's class, a word the block's own code names and the platform reads back, and scrubs all of it when text does not hold refused as written or a secret reads across the class's edge.
func scrubAroundClass(text string, refused *Refused, secrets []string) string {
	before, after, found := strings.Cut(text, refused.Error())
	if !found {
		return safe.Scrub(text, secrets...)
	}
	head := safe.Scrub(before, secrets...)
	maskedRefusal := &Refused{Class: refused.Class, Detail: safe.Scrub(refused.Detail, secrets...)}
	masked := head + maskedRefusal.Error() + safe.Scrub(after, secrets...)
	classStart := len(head) + len(refusedPrefix)
	if secretOutsideSpan(masked, classStart, classStart+len(refused.wireClass()), secrets) {
		log.Printf("handler: a secret reads across the edge of refusal class %s, so the refusal is masked whole", safe.LogSafe(refused.wireClass(), secrets...))
		return safe.Scrub(text, secrets...)
	}
	return masked
}

// secretOutsideSpan reports whether a secret Scrub would mask occurs in text anywhere but wholly inside text[start:end], since pieces scrubbed apart can join into a secret.
func secretOutsideSpan(text string, start, end int, secrets []string) bool {
	for _, secret := range secrets {
		// A match starting before start ends by start+len-1, and a match ending after end starts at end-len+1 or later.
		left := text[:min(len(text), start+len(secret)-1)]
		right := text[min(len(text), max(0, end-len(secret)+1)):]
		if safe.Scrub(left, secret) != left || safe.Scrub(right, secret) != right {
			return true
		}
	}
	return false
}
