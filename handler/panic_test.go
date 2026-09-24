package handler_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// A Resource that panics is a fault in the block, not a refusal, so the
// Handler answers it the way it answers any other internal error: HTTP 500
// with a non-empty "error" and no "refused:<class>" statusDetail. The engine
// reads any status >= 500 as AttemptOperatorFailed and retries it under the
// same idempotency key, and the idempotency cache never stores a 500, so the
// retry reaches the Resource again. The Handler recovers the panic around
// the verb, inside the single-flight call, so the calling delivery and every
// waiter on the same key receive that one response.

// panicSecret is the secret value every panic test puts on the request and
// quotes in the panic value, so a test can prove it reaches neither the
// response nor the log.
const panicSecret = "top-secret-panic-db-password"

// panicMessage is the non-secret part of the panic value, which the log
// must carry so an operator can tell what the block was doing.
const panicMessage = "backend exploded mid-converge"

// panickingResource panics in every Resource method. When release is
// non-nil, Ensure first signals entered and then blocks on release, so a
// test can hold the call in flight while a second delivery joins it.
type panickingResource struct {
	mu          sync.Mutex
	ensureCalls int
	observeCall int
	deleteCalls int
	entered     chan struct{}
	release     chan struct{}
}

func (r *panickingResource) Ensure(ctx context.Context, d contract.Desired) error {
	r.mu.Lock()
	r.ensureCalls++
	r.mu.Unlock()
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.release != nil {
		<-r.release
	}
	panic(fmt.Sprintf("%s using credential %s", panicMessage, panicSecret))
}

func (r *panickingResource) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	r.mu.Lock()
	r.observeCall++
	r.mu.Unlock()
	panic(fmt.Sprintf("%s using credential %s", panicMessage, panicSecret))
}

func (r *panickingResource) Delete(ctx context.Context, id contract.Identity) error {
	r.mu.Lock()
	r.deleteCalls++
	r.mu.Unlock()
	panic(fmt.Sprintf("%s using credential %s", panicMessage, panicSecret))
}

func (r *panickingResource) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensureCalls
}

var _ handler.Resource = (*panickingResource)(nil)

// servedResult is what one delivery produced. escaped holds the panic value
// when ServeHTTP panicked instead of answering, which is the defect these
// tests pin: an answered request never sets it.
type servedResult struct {
	status  int
	body    []byte
	escaped any
}

// serveRecovering sends a pre-built request through h and recovers any
// panic that escapes ServeHTTP, so a handler that lets a Resource panic
// through fails the test instead of crashing the test binary. It touches no
// *testing.T, so it is safe to call from a goroutine.
func serveRecovering(h http.Handler, req *http.Request) (res servedResult) {
	w := httptest.NewRecorder()
	defer func() {
		if p := recover(); p != nil {
			res = servedResult{status: w.Code, body: w.Body.Bytes(), escaped: p}
		}
	}()
	h.ServeHTTP(w, req)
	return servedResult{status: w.Code, body: w.Body.Bytes()}
}

// panicEnvelope builds a signed delivery whose secrets carry panicSecret.
func panicEnvelope(t *testing.T, s *kittest.Signer, nonce string, extra map[string]any) *http.Request {
	t.Helper()
	fields := map[string]any{
		"blockId":        "block-panicky",
		"subscriptionId": "sub-panicky",
		"secrets":        map[string]any{"db": map[string]any{"password": panicSecret}},
	}
	for k, v := range extra {
		fields[k] = v
	}
	return s.HookRequest(t, "/hook", envelope(t, s, nonce, fields))
}

// assertPanicAnswer pins the response a panicking Resource must produce: an
// answered request (no escaped panic), status 500, a contract-conforming
// body whose error says a panic happened, no refusal class, and no secret.
func assertPanicAnswer(t *testing.T, who string, res servedResult) {
	t.Helper()
	if res.escaped != nil {
		t.Fatalf("%s: the Resource's panic escaped ServeHTTP instead of being answered: %v", who, res.escaped)
	}
	if res.status != http.StatusInternalServerError {
		t.Fatalf("%s: status = %d, want 500 for a panicking Resource, body: %s", who, res.status, res.body)
	}
	assertConforms(t, res.body)
	resp := decodeResponse(t, res.body)
	if resp.Error == "" {
		t.Errorf("%s: error is empty, want a message the engine can surface, body: %s", who, res.body)
	}
	if !strings.Contains(strings.ToLower(resp.Error), "panic") {
		t.Errorf("%s: error = %q, want it to say the block panicked", who, resp.Error)
	}
	if class := handler.RefusedClass(resp.StatusDetail); class != "" {
		t.Errorf("%s: statusDetail carries refusal class %q, want none: a panic is a fault, not a refusal", who, class)
	}
	kittest.AssertNoSecret(t, who+" response body", string(res.body), panicSecret)
}

// TestServe_PanickingResourceAnswers500WithABody pins that a panicking
// Resource answers the calling delivery with status 500 and a body, for
// every verb that reaches the Resource and whether or not the delivery
// carries an idempotency key, since the two take different paths to the
// Resource.
func TestServe_PanickingResourceAnswers500WithABody(t *testing.T) {
	for _, op := range []string{"subscribe", "upgrade", "validate", "unsubscribe"} {
		for _, key := range []string{"", "idem-panic-" + op} {
			name := op + "/no-idempotency-key"
			if key != "" {
				name = op + "/with-idempotency-key"
			}
			t.Run(name, func(t *testing.T) {
				s := kittest.NewSigner(t, "kid-1", 7)
				r := &panickingResource{}
				h := newHandler(t, r, s)

				extra := map[string]any{"operation": op}
				if key != "" {
					extra["idempotencyKey"] = key
				}
				res := serveRecovering(h, panicEnvelope(t, s, "nonce-panic-"+name, extra))
				assertPanicAnswer(t, "calling delivery", res)
			})
		}
	}
}

// TestServe_PanickingResourceIsNotCachedRetryReachesTheResourceAgain pins
// that the 500 a panic produces stays retryable: a later delivery of the
// same key calls the Resource again rather than replaying the failure.
func TestServe_PanickingResourceIsNotCachedRetryReachesTheResourceAgain(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	r := &panickingResource{}
	h := newHandler(t, r, s)

	extra := map[string]any{"idempotencyKey": "idem-panic-retry"}
	first := serveRecovering(h, panicEnvelope(t, s, "nonce-panic-retry-a", extra))
	assertPanicAnswer(t, "first delivery", first)

	second := serveRecovering(h, panicEnvelope(t, s, "nonce-panic-retry-b", extra))
	assertPanicAnswer(t, "retry", second)
	if got := r.calls(); got != 2 {
		t.Errorf("Ensure called %d times, want 2: a panic's 500 must not be cached under the key", got)
	}
}

// waitForSingleFlightWaiters polls every goroutine's stack until want
// goroutines sit inside the idempotency cache's single-flight call without
// being inside the Resource itself. The first delivery holds the in-flight
// entry for as long as the Resource blocks, so a delivery found inside the
// cache at that point can only join it as a waiter.
func waitForSingleFlightWaiters(t *testing.T, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		got := 0
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(g, "operator-kit/handler.(*idempotencyCache).") &&
				!strings.Contains(g, "(*panickingResource).Ensure") {
				got++
			}
		}
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d delivery(ies) joined the in-flight call as a waiter within %s, want %d", got, timeout, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// overlappingPanic starts one delivery that holds inside Ensure, joins a
// second delivery of the same key as a waiter, then lets Ensure panic. It
// returns both results, failing the test through its own timeout rather
// than hanging if either delivery never returns.
func overlappingPanic(t *testing.T, key string) (first, waiter servedResult, r *panickingResource) {
	t.Helper()
	s := kittest.NewSigner(t, "kid-1", 7)
	r = &panickingResource{entered: make(chan struct{}, 8), release: make(chan struct{})}
	h := newHandler(t, r, s)

	extra := map[string]any{"idempotencyKey": key}
	reqA := panicEnvelope(t, s, "nonce-"+key+"-a", extra)
	reqB := panicEnvelope(t, s, "nonce-"+key+"-b", extra)

	firstCh := make(chan servedResult, 1)
	waiterCh := make(chan servedResult, 1)

	go func() { firstCh <- serveRecovering(h, reqA) }()
	select {
	case <-r.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first delivery never reached Ensure")
	}

	go func() { waiterCh <- serveRecovering(h, reqB) }()
	waitForSingleFlightWaiters(t, 1, 5*time.Second)

	close(r.release)

	timeout := time.After(5 * time.Second)
	for got := 0; got < 2; got++ {
		select {
		case first = <-firstCh:
		case waiter = <-waiterCh:
		case <-timeout:
			t.Fatalf("a delivery was left waiting after the Resource panicked (%d of 2 returned)", got)
		}
	}
	if calls := r.calls(); calls != 1 {
		t.Fatalf("Ensure called %d times, want 1: the second delivery must have waited on the first, not run the verb", calls)
	}
	return first, waiter, r
}

// TestServe_PanickingResourceWaiterOnTheSameKeyGetsTheSameResponse pins
// that a delivery waiting on a key whose in-flight call panics receives the
// panic's 500 response, byte for byte what the calling delivery received,
// rather than the zero status the in-flight call held before its run
// function set one.
func TestServe_PanickingResourceWaiterOnTheSameKeyGetsTheSameResponse(t *testing.T) {
	first, waiter, _ := overlappingPanic(t, "idem-panic-waiter")

	if waiter.escaped == nil && waiter.status == 0 {
		t.Fatalf("waiter answered status 0, the zero value the in-flight call held when the Resource panicked")
	}
	assertPanicAnswer(t, "waiter", waiter)
	assertPanicAnswer(t, "calling delivery", first)
	if waiter.status != first.status {
		t.Errorf("waiter status = %d, want the calling delivery's %d", waiter.status, first.status)
	}
	if !bytes.Equal(waiter.body, first.body) {
		t.Errorf("waiter body = %s, want the calling delivery's exact body %s", waiter.body, first.body)
	}
}

// TestServe_PanickingResourceReleasesEveryWaiter pins the liveness
// guarantee on its own: when the in-flight call panics, every delivery
// waiting on its key returns, whatever it answers. It fails through
// overlappingPanic's timeout if a waiter is left blocked.
func TestServe_PanickingResourceReleasesEveryWaiter(t *testing.T) {
	overlappingPanic(t, "idem-panic-liveness")
}

// TestServe_PanickingResourceIsLoggedWithTheBlockAndNoSecret pins that a
// panic is logged with what an operator needs to find the block that raised
// it (the block id, the subscription id, the verb and the panic value) and
// that the log carries no secret from the request, including one the panic
// value itself quotes.
func TestServe_PanickingResourceIsLoggedWithTheBlockAndNoSecret(t *testing.T) {
	logBuf := captureLog(t)

	s := kittest.NewSigner(t, "kid-1", 7)
	r := &panickingResource{}
	h := newHandler(t, r, s)

	res := serveRecovering(h, panicEnvelope(t, s, "nonce-panic-log", map[string]any{"idempotencyKey": "idem-panic-log"}))
	assertPanicAnswer(t, "calling delivery", res)

	logged := logBuf.String()
	for _, want := range []string{"panic", "block-panicky", "sub-panicky", "subscribe", panicMessage} {
		if !strings.Contains(strings.ToLower(logged), strings.ToLower(want)) {
			t.Errorf("log does not carry %q, want it to identify the panicking block; log:\n%s", want, logged)
		}
	}
	kittest.AssertNoSecret(t, "log output", logged, panicSecret)
}
