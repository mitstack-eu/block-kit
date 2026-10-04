// Package events lets an operator push lifecycle events back to mitstack's
// ingest endpoint (MIT-1151: POST /v1/webhooks/operator-events), using the
// eventsCallback {url, token} the engine injects into every signed webhook
// envelope (MIT-1150).
//
// Advisory by construction (ADR 0042): the channel accelerates the lifecycle,
// it never carries it. EmitProgress/EmitCompleted/EmitFailed are no-ops — no
// HTTP call, no error — when the envelope carries no eventsCallback, so an
// older engine or a feature-off deployment behaves exactly as before.
//
// Extracted from the byte-identical events.go that MIT-1152 shipped by hand to
// every operator; this module is now its single implementation (MIT-1466).
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"time"
)

// Callback carries the ingest URL + bearer token mitstack injects into the
// signed webhook envelope so the operator can push lifecycle events back.
type Callback struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// Envelope is the subset of the operator's decoded request the emit helpers
// need. The operator builds one from its own request type — the kit deliberately
// does not know that type, so an operator may evolve its request struct without
// touching the kit.
type Envelope struct {
	SubscriptionID string
	Operation      string
	IdempotencyKey string
	Callback       *Callback
}

type envelopeKey struct{}

// WithEnvelope stashes the events envelope on ctx so downstream handler code can
// call EmitProgress/EmitCompleted/EmitFailed without threading the request
// through every call.
func WithEnvelope(ctx context.Context, env Envelope) context.Context {
	return context.WithValue(ctx, envelopeKey{}, &env)
}

// EnvelopeFrom returns the envelope WithEnvelope stashed on ctx, and false
// when ctx carries none. An envelope with no callback is still found, with a
// nil Callback, so a caller that needs the callback checks it itself.
func EnvelopeFrom(ctx context.Context) (Envelope, bool) {
	env, ok := ctx.Value(envelopeKey{}).(*Envelope)
	if !ok || env == nil {
		return Envelope{}, false
	}
	return *env, true
}

// httpClient is used to POST events; kept short-timeout so a slow/dead ingest
// endpoint never blocks operator lifecycle handling for long.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// operatorEvent mirrors mitstack's operatorEventIngestBody (MIT-1151).
type operatorEvent struct {
	SubscriptionID string `json:"subscriptionId"`
	Token          string `json:"token"`
	Operation      string `json:"operation"`
	Status         string `json:"status"`
	Detail         string `json:"detail"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// EmitProgress reports a "progress" event with the given detail.
func EmitProgress(ctx context.Context, detail string) error { return emit(ctx, "progress", detail) }

// EmitCompleted reports a "completed" event with no detail.
func EmitCompleted(ctx context.Context) error { return emit(ctx, "completed", "") }

// EmitFailed reports a "failed" event carrying the given failure detail.
func EmitFailed(ctx context.Context, detail string) error { return emit(ctx, "failed", detail) }

// emit no-ops when the context carries no events envelope, or the envelope's
// callback is nil (older engine / feature off). Otherwise it synchronously POSTs
// the scrubbed event to Callback.URL and returns any genuine send failure.
func emit(ctx context.Context, status, detail string) error {
	env, ok := ctx.Value(envelopeKey{}).(*Envelope)
	if !ok || env == nil || env.Callback == nil {
		return nil
	}

	ev := operatorEvent{
		SubscriptionID: env.SubscriptionID,
		Token:          env.Callback.Token,
		Operation:      env.Operation,
		Status:         status,
		Detail:         ScrubDetail(detail),
		IdempotencyKey: env.IdempotencyKey,
	}

	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.Callback.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

// secretLikeKeyPattern matches `key=value` or `key: value` assignments where the
// key looks like a secret/credential field.
var secretLikeKeyPattern = regexp.MustCompile(`(?i)(token|secret|password|passwd|apikey|api_key|credential|private_key|privatekey)\s*[:=]\s*\S+`)

var assignSepPattern = regexp.MustCompile(`[:=]`)

// ScrubDetail redacts secret-shaped `key=value`/`key: value` assignments from an
// event detail string before it is marshalled and sent off-process.
func ScrubDetail(detail string) string {
	return secretLikeKeyPattern.ReplaceAllStringFunc(detail, func(match string) string {
		idx := assignSepPattern.FindStringIndex(match)
		if idx == nil {
			return match
		}
		return match[:idx[1]] + " [REDACTED]"
	})
}
