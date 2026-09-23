// observewithrequest_test.go pins the route a kit/v1 block reaches through
// handler.RequestObserver: a Resource implementing it sees the request's
// full Desired — secrets included — on validate, and on every settle poll a
// subscribe, upgrade or unsubscribe drives. A plain Resource that does not
// implement it keeps calling Observe(ctx, Identity) exactly as before, and
// the secrets one verb's polls saw are never retained anywhere the handler
// could hand them to an unrelated later call — the idempotency cache
// included.
//
// requestObserverFake structurally mirrors kittest.FakeResource but also
// implements handler.RequestObserver, the same reasoning handover_test.go
// gives for not extending the shared kittest fake: whether RequestObserver
// belongs on the shipped FakeResource too is a call for whoever owns that
// package, not one this test file should make by editing it directly.
package handler_test

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"sync"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// requestObserverFake is a Resource that also implements
// handler.RequestObserver. PresentAfter/AbsentAfter let a test force a known
// number of settle polls before the loop stops, so "at least two polls" is
// exact rather than incidental: with PresentAfter (or AbsentAfter) set to N,
// ObserveWithRequest reports something other than the target state for the
// first N-1 calls and the target state on the Nth.
type requestObserverFake struct {
	mu sync.Mutex

	PresentAfter int // 0 means every call reports Present.
	AbsentAfter  int // 0 means every call reports Absent, unless PresentAfter is set.

	Outputs       map[string]string
	SecretOutputs map[string]string
	StatusDetail  string

	LastDesired             contract.Desired
	SecretsSeen             []map[string]map[string]string
	ObserveWithRequestCalls int
	ObserveCalls            int
	EnsureCalls             int
	DeleteCalls             int
}

func (f *requestObserverFake) Ensure(ctx context.Context, d contract.Desired) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.EnsureCalls++
	return nil
}

// Observe is the plain path handler.Resource still requires. It must never
// be called for this fake once the Handler sees it also implements
// RequestObserver.
func (f *requestObserverFake) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ObserveCalls++
	return contract.Observation{State: contract.Present}, nil
}

func (f *requestObserverFake) Delete(ctx context.Context, id contract.Identity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DeleteCalls++
	return nil
}

// ObserveWithRequest is handler.RequestObserver's method: it receives the
// request's full Desired — secrets included — and records it, plus every
// secrets map it has ever seen, so a test can assert every poll saw the same
// verb's secrets.
func (f *requestObserverFake) ObserveWithRequest(ctx context.Context, d contract.Desired) (contract.Observation, error) {
	f.mu.Lock()
	f.ObserveWithRequestCalls++
	f.LastDesired = d
	f.SecretsSeen = append(f.SecretsSeen, d.Secrets)
	n := f.ObserveWithRequestCalls
	f.mu.Unlock()

	state := contract.Present
	switch {
	case f.PresentAfter > 0:
		if n < f.PresentAfter {
			state = contract.Unknown
		} else {
			state = contract.Present
		}
	case f.AbsentAfter > 0:
		if n < f.AbsentAfter {
			state = contract.Unknown
		} else {
			state = contract.Absent
		}
	}

	return contract.Observation{State: state, Outputs: f.Outputs, SecretOutputs: f.SecretOutputs, StatusDetail: f.StatusDetail}, nil
}

var _ handler.Resource = (*requestObserverFake)(nil)
var _ handler.RequestObserver = (*requestObserverFake)(nil)

// TestServe_ValidateObserveWithRequestReceivesTheValidateRequestSecrets pins
// the first acceptance criterion: a validate request carrying
// secrets.github.token reaches a RequestObserver Resource's
// ObserveWithRequest, and the plain Observe is never called instead.
func TestServe_ValidateObserveWithRequestReceivesTheValidateRequestSecrets(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const tokenValue = "tok-validate-observes-9f3a"
	f := &requestObserverFake{}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-observe-validate-secrets", map[string]any{
		"operation": "validate",
		"secrets":   map[string]any{"github": map[string]any{"token": tokenValue}},
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	if f.ObserveWithRequestCalls != 1 {
		t.Fatalf("ObserveWithRequestCalls = %d, want exactly 1", f.ObserveWithRequestCalls)
	}
	if f.ObserveCalls != 0 {
		t.Errorf("ObserveCalls = %d, want 0 — the plain Observe must never run once RequestObserver is implemented", f.ObserveCalls)
	}
	if got := f.LastDesired.Secrets["github"]["token"]; got != tokenValue {
		t.Errorf(`ObserveWithRequest saw Secrets["github"]["token"] = %q, want %q`, got, tokenValue)
	}
}

// TestServe_SubscribeSettlePollsEachReceiveTheSubscribeSecrets pins the
// subscribe half of the second acceptance criterion: driven through at
// least two settle polls, every poll's ObserveWithRequest call carries the
// subscribe request's own secrets.
func TestServe_SubscribeSettlePollsEachReceiveTheSubscribeSecrets(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const tokenValue = "tok-subscribe-poll-7c21"
	const budget = 3
	f := &requestObserverFake{PresentAfter: budget} // Present only on the final (3rd) poll
	h := newHandler(t, f, s, func(c *handler.Config) { c.VerbBudget = budget })

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-observe-subscribe-polls", map[string]any{
		"operation": "subscribe",
		"secrets":   map[string]any{"github": map[string]any{"token": tokenValue}},
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	if f.ObserveWithRequestCalls < 2 {
		t.Fatalf("ObserveWithRequestCalls = %d, want at least 2 settle polls", f.ObserveWithRequestCalls)
	}
	for i, secrets := range f.SecretsSeen {
		if got := secrets["github"]["token"]; got != tokenValue {
			t.Errorf("poll #%d saw Secrets[github][token] = %q, want %q", i+1, got, tokenValue)
		}
	}
}

// TestServe_UnsubscribeSettlePollsEachReceiveTheUnsubscribeSecrets mirrors
// the above for unsubscribe/settleAbsent, completing the second acceptance
// criterion.
func TestServe_UnsubscribeSettlePollsEachReceiveTheUnsubscribeSecrets(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const tokenValue = "tok-unsubscribe-poll-4e88"
	const budget = 3
	f := &requestObserverFake{AbsentAfter: budget} // Absent only on the final (3rd) poll
	h := newHandler(t, f, s, func(c *handler.Config) { c.VerbBudget = budget })

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-observe-unsubscribe-polls", map[string]any{
		"operation": "unsubscribe",
		"secrets":   map[string]any{"github": map[string]any{"token": tokenValue}},
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	if f.ObserveWithRequestCalls < 2 {
		t.Fatalf("ObserveWithRequestCalls = %d, want at least 2 settle polls", f.ObserveWithRequestCalls)
	}
	for i, secrets := range f.SecretsSeen {
		if got := secrets["github"]["token"]; got != tokenValue {
			t.Errorf("poll #%d saw Secrets[github][token] = %q, want %q", i+1, got, tokenValue)
		}
	}
}

// TestServe_PlainResourceObserveIsUnaffectedWhenSecretsArePresent proves the
// third hard requirement: a Resource implementing only handler.Resource — no
// RequestObserver — keeps calling Observe(ctx, Identity) exactly as before,
// even when the request carries secrets. kittest.FakeResource does not
// implement RequestObserver, so this is the plain-Resource control case.
func TestServe_PlainResourceObserveIsUnaffectedWhenSecretsArePresent(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{Outputs: map[string]string{"url": "https://example.test"}}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-plain-resource-untouched", map[string]any{
		"secrets": map[string]any{"github": map[string]any{"token": "tok-plain-untouched-11aa"}},
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	if f.ObserveCalls != 1 {
		t.Errorf("ObserveCalls = %d, want exactly 1 — a plain Resource must keep being observed the old way", f.ObserveCalls)
	}
	if f.EnsureCalls != 1 {
		t.Errorf("EnsureCalls = %d, want exactly 1", f.EnsureCalls)
	}
	resp := decodeResponse(t, raw)
	if resp.Outputs["url"] != "https://example.test" {
		t.Errorf("Outputs[url] = %q, want the block's own output, unaffected by the new interface", resp.Outputs["url"])
	}
}

// TestServe_PollSecretsFromOneVerbNeverReachALaterUnrelatedRequest proves
// that a verb's secrets live only for the duration of that verb's own
// request: a second, unrelated request that carries no secrets of its own
// must never see the first verb's secrets show up in the Desired its
// ObserveWithRequest is handed. A Handler that stashed the first verb's
// secrets in any field of its own state, to reuse across calls, would leak
// them here.
func TestServe_PollSecretsFromOneVerbNeverReachALaterUnrelatedRequest(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const tokenValue = "tok-scope-should-not-leak-2b6f"
	f := &requestObserverFake{}
	h := newHandler(t, f, s)

	status1, raw1 := postHook(t, h, s, envelope(t, s, "nonce-scope-a", map[string]any{
		"operation":      "validate",
		"subscriptionId": "sub-scope-a",
		"secrets":        map[string]any{"github": map[string]any{"token": tokenValue}},
	}))
	if status1 != http.StatusOK {
		t.Fatalf("first request: status = %d, body: %s", status1, raw1)
	}
	if got := f.LastDesired.Secrets["github"]["token"]; got != tokenValue {
		t.Fatalf("first request did not reach ObserveWithRequest with its own secret: got %q", got)
	}

	status2, raw2 := postHook(t, h, s, envelope(t, s, "nonce-scope-b", map[string]any{
		"operation":      "validate",
		"subscriptionId": "sub-scope-b",
	}))
	if status2 != http.StatusOK {
		t.Fatalf("second request: status = %d, body: %s", status2, raw2)
	}
	if len(f.LastDesired.Secrets) != 0 {
		t.Errorf("second request's Desired carried Secrets = %v, want none — the first verb's secrets must not persist in Handler state", f.LastDesired.Secrets)
	}
}

// TestServe_IdempotencyCacheNeverStoresOrLeaksVerbSecrets proves the other
// half of "stored nowhere, the idempotency cache included": a subscribe
// carrying a secret, replayed a second time under the same idempotencyKey,
// never surfaces that secret in either delivery's response body or in the
// log — the cache stores only a status and response bytes (idempotency.go),
// and the response bytes it stores must already be clean.
func TestServe_IdempotencyCacheNeverStoresOrLeaksVerbSecrets(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const tokenValue = "tok-idempotency-cache-should-not-leak-5d9c"
	f := &requestObserverFake{Outputs: map[string]string{"status": "ok"}}
	h := newHandler(t, f, s)

	var logBuf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	extra := map[string]any{
		"idempotencyKey": "idem-secret-cache-1",
		"subscriptionId": "sub-idem-secret-1",
		"secrets":        map[string]any{"github": map[string]any{"token": tokenValue}},
	}

	status1, raw1 := postHook(t, h, s, envelope(t, s, "nonce-idem-secret-a", extra))
	if status1 != http.StatusOK {
		t.Fatalf("first delivery: status = %d, body: %s", status1, raw1)
	}

	status2, raw2 := postHook(t, h, s, envelope(t, s, "nonce-idem-secret-b", extra))
	if status2 != status1 {
		t.Errorf("replay status = %d, want the first delivery's status %d", status2, status1)
	}
	if !bytes.Equal(raw1, raw2) {
		t.Errorf("replay body = %s, want the exact first-delivery body %s", raw2, raw1)
	}
	if f.ObserveWithRequestCalls != 1 {
		t.Errorf("ObserveWithRequestCalls = %d, want exactly 1 — the replay must never call the Resource again", f.ObserveWithRequestCalls)
	}

	kittest.AssertNoSecret(t, "first delivery response body", string(raw1), tokenValue)
	kittest.AssertNoSecret(t, "replayed response body", string(raw2), tokenValue)
	kittest.AssertNoSecret(t, "log output", logBuf.String(), tokenValue)
}
