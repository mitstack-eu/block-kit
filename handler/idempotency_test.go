package handler_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/mitstack-eu/operator-kit/kittest"
)

// TestServe_IdempotencyReplaysTheFirstResponseForTheSameKey pins that a
// repeated request carrying the same idempotencyKey replays the first
// status and response bytes without calling the Resource again. The two
// deliveries use different nonces, exactly as a genuine retry from the
// engine would (a repeated nonce is rejected by the replay guard
// regardless of idempotencyKey).
func TestServe_IdempotencyReplaysTheFirstResponseForTheSameKey(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	h := newHandler(t, f, s)

	extra := map[string]any{"idempotencyKey": "idem-1", "subscriptionId": "sub-1"}
	status1, raw1 := postHook(t, h, s, envelope("nonce-idem-1a", extra))
	if status1 != http.StatusOK {
		t.Fatalf("first delivery: status = %d, body: %s", status1, raw1)
	}
	if f.EnsureCalls != 1 {
		t.Fatalf("EnsureCalls after first delivery = %d, want 1", f.EnsureCalls)
	}

	status2, raw2 := postHook(t, h, s, envelope("nonce-idem-1b", extra))
	if status2 != status1 {
		t.Errorf("replay status = %d, want the first delivery's status %d", status2, status1)
	}
	if !bytes.Equal(raw1, raw2) {
		t.Errorf("replay body = %s, want the exact first-delivery body %s", raw2, raw1)
	}
	if f.EnsureCalls != 1 {
		t.Errorf("EnsureCalls after the replay = %d, want still 1 — the Resource must not be called again", f.EnsureCalls)
	}
}

// TestServe_IdempotencyDifferentKeyCallsTheResourceAgain pins the other
// half: a different idempotencyKey for the same subscription is a distinct
// request and must reach the Resource again.
func TestServe_IdempotencyDifferentKeyCallsTheResourceAgain(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	h := newHandler(t, f, s)

	postHook(t, h, s, envelope("nonce-idem-2a", map[string]any{"idempotencyKey": "idem-A", "subscriptionId": "sub-1"}))
	if f.EnsureCalls != 1 {
		t.Fatalf("EnsureCalls after first delivery = %d, want 1", f.EnsureCalls)
	}

	postHook(t, h, s, envelope("nonce-idem-2b", map[string]any{"idempotencyKey": "idem-B", "subscriptionId": "sub-1"}))
	if f.EnsureCalls != 2 {
		t.Errorf("EnsureCalls after a different idempotencyKey = %d, want 2 — a distinct key is a distinct request", f.EnsureCalls)
	}
}

// TestServe_IdempotencyEmptyKeyIsNeverCached pins "An empty key is never
// cached": two requests with no idempotencyKey at all must each reach the
// Resource, even though every other field is identical.
func TestServe_IdempotencyEmptyKeyIsNeverCached(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	h := newHandler(t, f, s)

	postHook(t, h, s, envelope("nonce-idem-3a", nil))
	postHook(t, h, s, envelope("nonce-idem-3b", nil))

	if f.EnsureCalls != 2 {
		t.Errorf("EnsureCalls = %d, want 2 — an empty idempotencyKey must never be cached/replayed", f.EnsureCalls)
	}
}

// TestServe_IdempotencyKeyedPerSubscription_NoCrossSubscriptionCollision is
// the multi-tenancy proof the CONTRACT asks for: two different
// subscriptions using the SAME idempotencyKey must not replay each other's
// response, so the cache must be keyed on (idempotencyKey, subscriptionId)
// together, not on idempotencyKey alone.
func TestServe_IdempotencyKeyedPerSubscription_NoCrossSubscriptionCollision(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	h := newHandler(t, f, s)

	sharedKey := "idem-shared"

	statusA, rawA := postHook(t, h, s, envelope("nonce-tenant-A", map[string]any{
		"idempotencyKey": sharedKey, "subscriptionId": "sub-A", "orgId": "org-A",
	}))
	if statusA != http.StatusOK {
		t.Fatalf("subscription A: status = %d, body: %s", statusA, rawA)
	}
	if f.EnsureCalls != 1 {
		t.Fatalf("EnsureCalls after subscription A = %d, want 1", f.EnsureCalls)
	}

	statusB, rawB := postHook(t, h, s, envelope("nonce-tenant-B", map[string]any{
		"idempotencyKey": sharedKey, "subscriptionId": "sub-B", "orgId": "org-B",
	}))
	if statusB != http.StatusOK {
		t.Fatalf("subscription B: status = %d, body: %s", statusB, rawB)
	}
	if f.EnsureCalls != 2 {
		t.Errorf("EnsureCalls after subscription B = %d, want 2 — a different subscription sharing the same "+
			"idempotencyKey must still call the Resource; the cache must be keyed on (idempotencyKey, subscriptionId)", f.EnsureCalls)
	}

	// A third delivery replaying subscription A's own key+subscription must
	// still replay correctly — proving the fix above did not simply widen
	// the cache key into never matching at all.
	statusA2, rawA2 := postHook(t, h, s, envelope("nonce-tenant-A2", map[string]any{
		"idempotencyKey": sharedKey, "subscriptionId": "sub-A", "orgId": "org-A",
	}))
	if statusA2 != statusA || !bytes.Equal(rawA2, rawA) {
		t.Errorf("re-delivery for subscription A did not replay: status=%d body=%s, want status=%d body=%s",
			statusA2, rawA2, statusA, rawA)
	}
	if f.EnsureCalls != 2 {
		t.Errorf("EnsureCalls after replaying subscription A = %d, want still 2", f.EnsureCalls)
	}
}
