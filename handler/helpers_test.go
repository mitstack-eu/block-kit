// Package handler_test exercises the kit's lifecycle webhook Handler
// end to end: real signed requests in, the exact Resource/hook call
// sequence and wire response bytes out. Every helper here builds
// requests and handlers the same way so a defect shows up as a red test,
// not a fixture divergence between test files.
package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
	"github.com/mitstack-eu/operator-kit/webhook"
)

// noopSleep is passed as Config.Sleep so a test never waits on wall-clock
// time, however small the poll interval — CONTRACT: "Deterministic: ...
// Config.Sleep as a no-op so no test waits on wall-clock time."
func noopSleep(context.Context, time.Duration) {}

// baseConfig returns a Config wired for a deterministic, hermetic test: a
// small VerbBudget, a no-op Sleep, and JWKS served by s. Callers mutate the
// returned value before calling handler.New.
func baseConfig(t *testing.T, r handler.Resource, s *kittest.Signer) handler.Config {
	t.Helper()
	return handler.Config{
		Resource:     r,
		JWKSURL:      s.JWKSServer(t),
		VerbBudget:   3,
		PollInterval: time.Millisecond,
		Sleep:        noopSleep,
	}
}

// newHandler builds a Handler from a fresh Signer and a Resource, applying
// mutate (if any) to the Config before calling handler.New, and fails the
// test if New errors — for tests where New is expected to succeed. r takes
// the handler.Resource interface rather than *kittest.FakeResource
// specifically, so a test that needs a Resource kittest does not provide
// (a blocking or context-cancelling fake, say) still goes through this one
// shared constructor instead of hand-rolling handler.New.
func newHandler(t *testing.T, r handler.Resource, s *kittest.Signer, mutate ...func(*handler.Config)) *handler.Handler {
	t.Helper()
	cfg := baseConfig(t, r, s)
	for _, m := range mutate {
		m(&cfg)
	}
	h, err := handler.New(cfg)
	if err != nil {
		t.Fatalf("handler.New: unexpected error: %v", err)
	}
	return h
}

// envelope builds a minimal but DecodeRequest-complete signed-webhook body:
// operation/subscriptionId/blockId/orgId plus nonce/createdAt (added by
// kittest.Envelope) — the full required set contract.DecodeRequest checks —
// merged with extra, which may override any of the defaults. It also mints
// and attaches a valid OBO token signed by s (unless extra already sets
// "obo" itself), so every existing call site keeps exercising the verb
// behaviour it already pins instead of tripping the OBO-required boundary.
// A test that wants a body with NO obo field at all — the boundary itself —
// builds it with kittest.Envelope directly instead of this helper; see
// obo_required_test.go.
func envelope(t *testing.T, s *kittest.Signer, nonce string, extra map[string]any) []byte {
	t.Helper()
	fields := map[string]any{
		"operation":      "subscribe",
		"subscriptionId": "sub-1",
		"blockId":        "block-1",
		"orgId":          "org-1",
	}
	for k, v := range extra {
		fields[k] = v
	}
	if _, hasOBO := fields["obo"]; !hasOBO {
		subID, _ := fields["subscriptionId"].(string)
		orgID, _ := fields["orgId"].(string)
		appID, _ := fields["appId"].(string)
		tok, err := webhook.MintOBO(s.Private, s.Kid, webhook.OBOClaims{
			OrgID: orgID, AppID: appID, SubscriptionID: subID,
		}, time.Hour, time.Now())
		if err != nil {
			t.Fatalf("minting a default OBO token for envelope(): %v", err)
		}
		fields["obo"] = tok
	}
	return kittest.Envelope(nonce, time.Now(), fields)
}

// postHook signs body with s and sends it through h's ServeHTTP, returning
// the recorded status and the exact response bytes written.
func postHook(t *testing.T, h http.Handler, s *kittest.Signer, body []byte) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, s.HookRequest(t, "/hook", body))
	return w.Code, w.Body.Bytes()
}

// postHookWithContext is postHook with the request's context replaced by
// ctx, so a test can cancel the context a verb runs under — the settle-loop
// cancellation proofs need this; postHook alone always runs on a live
// background context.
func postHookWithContext(t *testing.T, h http.Handler, s *kittest.Signer, body []byte, ctx context.Context) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	r := s.HookRequest(t, "/hook", body).WithContext(ctx)
	h.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

// decodeResponse decodes raw as a contract.Response, failing the test if it
// does not decode — every response byte this package asserts on must at
// least be a well-formed response envelope.
func decodeResponse(t *testing.T, raw []byte) contract.Response {
	t.Helper()
	resp, err := contract.DecodeResponse(raw)
	if err != nil {
		t.Fatalf("response does not decode as a contract.Response: %v\nbody: %s", err, raw)
	}
	return resp
}

// assertConforms runs raw through kittest.CheckResponse so every response
// this package asserts on is proven contract-conforming, not merely
// decodable.
func assertConforms(t *testing.T, raw []byte) {
	t.Helper()
	if problems := kittest.CheckResponse(raw); len(problems) != 0 {
		t.Errorf("response does not conform to the lifecycle webhook response contract: %v\nbody: %s", problems, raw)
	}
}

func zeroCalls(t *testing.T, f *kittest.FakeResource) {
	t.Helper()
	if f.EnsureCalls != 0 || f.ObserveCalls != 0 || f.DeleteCalls != 0 {
		t.Errorf("expected zero Resource calls, got Ensure=%d Observe=%d Delete=%d",
			f.EnsureCalls, f.ObserveCalls, f.DeleteCalls)
	}
}
