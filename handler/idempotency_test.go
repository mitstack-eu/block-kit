package handler_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
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
	status1, raw1 := postHook(t, h, s, envelope(t, s, "nonce-idem-1a", extra))
	if status1 != http.StatusOK {
		t.Fatalf("first delivery: status = %d, body: %s", status1, raw1)
	}
	if f.EnsureCalls != 1 {
		t.Fatalf("EnsureCalls after first delivery = %d, want 1", f.EnsureCalls)
	}

	status2, raw2 := postHook(t, h, s, envelope(t, s, "nonce-idem-1b", extra))
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

	postHook(t, h, s, envelope(t, s, "nonce-idem-2a", map[string]any{"idempotencyKey": "idem-A", "subscriptionId": "sub-1"}))
	if f.EnsureCalls != 1 {
		t.Fatalf("EnsureCalls after first delivery = %d, want 1", f.EnsureCalls)
	}

	postHook(t, h, s, envelope(t, s, "nonce-idem-2b", map[string]any{"idempotencyKey": "idem-B", "subscriptionId": "sub-1"}))
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

	postHook(t, h, s, envelope(t, s, "nonce-idem-3a", nil))
	postHook(t, h, s, envelope(t, s, "nonce-idem-3b", nil))

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

	statusA, rawA := postHook(t, h, s, envelope(t, s, "nonce-tenant-A", map[string]any{
		"idempotencyKey": sharedKey, "subscriptionId": "sub-A", "orgId": "org-A",
	}))
	if statusA != http.StatusOK {
		t.Fatalf("subscription A: status = %d, body: %s", statusA, rawA)
	}
	if f.EnsureCalls != 1 {
		t.Fatalf("EnsureCalls after subscription A = %d, want 1", f.EnsureCalls)
	}

	statusB, rawB := postHook(t, h, s, envelope(t, s, "nonce-tenant-B", map[string]any{
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
	statusA2, rawA2 := postHook(t, h, s, envelope(t, s, "nonce-tenant-A2", map[string]any{
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

// --- A failed response is never cached (MIT-3070 review) --------------------
//
// The engine keeps one IdempotencyKey across every retry of a call
// (webhook.go:304,364) and a 5xx stays retryable (webhook.go:455). Caching a
// 500 under that key would make every later retry replay the cached failure
// without ever calling the Resource again, turning one transient error into
// a permanent one. The cache therefore stores a response only for a
// deterministic outcome — a 2xx or the 400 a *Refused produces — and a 500
// leaves no entry behind.

// flakyResource fails its first Ensure call and succeeds every call after,
// modelling the transient backend error the engine's retry exists for.
type flakyResource struct {
	mu          sync.Mutex
	ensureCalls int
}

func (f *flakyResource) Ensure(context.Context, contract.Desired) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls++
	if f.ensureCalls == 1 {
		return errors.New("transient backend error")
	}
	return nil
}

func (f *flakyResource) Observe(context.Context, contract.Identity) (contract.Observation, error) {
	return contract.Observation{State: contract.Present}, nil
}

func (f *flakyResource) Delete(context.Context, contract.Identity) error { return nil }

func (f *flakyResource) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ensureCalls
}

// TestServe_Idempotency500IsNeverCachedRetryRunsTheResourceAgain pins that a
// 500 outcome leaves no cache entry: a repeat of the same key calls Ensure
// again, and — since the engine's client keeps retrying under the same key
// until it gets a deterministic answer — the second attempt's success is
// what the retry actually receives, not a replayed failure.
func TestServe_Idempotency500IsNeverCachedRetryRunsTheResourceAgain(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	r := &flakyResource{}
	h := newHandler(t, r, s)

	extra := map[string]any{"idempotencyKey": "idem-500", "subscriptionId": "sub-1"}

	status1, raw1 := postHook(t, h, s, envelope(t, s, "nonce-500-a", extra))
	if status1 != http.StatusInternalServerError {
		t.Fatalf("first delivery: status = %d, want 500, body: %s", status1, raw1)
	}
	if r.calls() != 1 {
		t.Fatalf("EnsureCalls after the first (failing) delivery = %d, want 1", r.calls())
	}

	status2, raw2 := postHook(t, h, s, envelope(t, s, "nonce-500-b", extra))
	if r.calls() != 2 {
		t.Errorf("EnsureCalls after the retry = %d, want 2 — a 500 must never be cached, so the retry must reach the Resource again", r.calls())
	}
	if status2 != http.StatusOK {
		t.Errorf("retry status = %d, want 200 — the retry's own (successful) answer, not the cached 500, body: %s", status2, raw2)
	}
	resp := decodeResponse(t, raw2)
	if resp.Error != "" {
		t.Errorf("retry Error = %q, want empty — the second attempt succeeded and must not carry the first attempt's failure", resp.Error)
	}
}

// The other half of "a 2xx and a 400 refusal are still replayed without
// calling it" needs no new test here: the pre-existing
// TestServe_IdempotencyReplaysTheFirstResponseForTheSameKey above already
// pins the 2xx case, and the current cache implementation already caches
// every status unconditionally (including a *Refused's 400), so a new test
// asserting that a 400 refusal is replayed would pass unchanged today — it
// would not be pinning a correction, only restating existing, unchanged
// behaviour. See NOTES in the handback for the caveat this leaves for
// code-author: the fix must keep caching a 400 refusal while it stops
// caching a 500, not simply narrow the cache to 2xx only.

// --- One key runs the verb once, even concurrently (MIT-3070 review) --------
//
// The cache is written only after the verb returns, so two deliveries of one
// key that overlap in flight both currently reach the Resource — exactly
// what happens when the engine's client times out while the handler is
// still inside Ensure and retries with the same key. The fix makes the
// cache single-flight: a delivery whose key is already in flight waits for
// the first to finish and answers with its response.

// blockingResource lets a test hold Ensure open until it chooses to release
// it, so two concurrent deliveries of the same key can be driven to overlap
// deterministically. entered is signalled on every Ensure call (buffered so
// a wrongly-duplicated call never blocks the Resource itself), which is what
// lets the test detect a second call arriving before the first is released.
type blockingResource struct {
	mu          sync.Mutex
	ensureCalls int
	entered     chan struct{}
	release     chan struct{}
}

func newBlockingResource() *blockingResource {
	return &blockingResource{entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func (r *blockingResource) Ensure(ctx context.Context, d contract.Desired) error {
	r.mu.Lock()
	r.ensureCalls++
	r.mu.Unlock()
	r.entered <- struct{}{}
	<-r.release
	return nil
}

func (r *blockingResource) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	return contract.Observation{State: contract.Present}, nil
}

func (r *blockingResource) Delete(ctx context.Context, id contract.Identity) error { return nil }

func (r *blockingResource) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensureCalls
}

// TestServe_IdempotencySingleFlightSameKeyCallsEnsureOnce fires two
// deliveries of one idempotency key concurrently, releases the Resource only
// once both are in flight, and asserts Ensure ran exactly once and both
// deliveries received the identical response. It is meaningful under -race:
// every field blockingResource shares across goroutines is either a channel
// or guarded by its own mutex, so a data race here can only come from a
// handler that reads/writes the idempotency cache without the synchronization
// the fix requires.
func TestServe_IdempotencySingleFlightSameKeyCallsEnsureOnce(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	r := newBlockingResource()
	h := newHandler(t, r, s)

	extra := map[string]any{"idempotencyKey": "idem-sf", "subscriptionId": "sub-1"}

	type result struct {
		status int
		body   []byte
	}
	results := make(chan result, 2)

	go func() {
		status, raw := postHook(t, h, s, envelope(t, s, "nonce-sf-a", extra))
		results <- result{status, raw}
	}()

	select {
	case <-r.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first delivery never reached Ensure")
	}

	go func() {
		status, raw := postHook(t, h, s, envelope(t, s, "nonce-sf-b", extra))
		results <- result{status, raw}
	}()

	// Give the second, concurrent delivery a window to reach Ensure a second
	// time while the first call is still blocked — the exact defect: the
	// cache is written only after Ensure returns, so nothing yet stops a
	// second in-flight delivery of the same key from calling the Resource.
	select {
	case <-r.entered:
		t.Error("a second concurrent delivery of the same key reached Ensure before the first one returned — the cache must be single-flight")
	case <-time.After(150 * time.Millisecond):
	}

	close(r.release)

	r1 := <-results
	r2 := <-results

	if calls := r.calls(); calls != 1 {
		t.Errorf("Ensure called %d times, want exactly 1 for two concurrent deliveries of the same key", calls)
	}
	if r1.status != r2.status {
		t.Errorf("concurrent deliveries got different statuses: %d vs %d", r1.status, r2.status)
	}
	if !bytes.Equal(r1.body, r2.body) {
		t.Errorf("concurrent deliveries got different bodies: %s vs %s", r1.body, r2.body)
	}
}
