package handler_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// TestServe_SubscribePresentWithinBudgetAnswers200AbsentFalse pins the
// settle happy path: a FakeResource that is Present as soon as Ensure runs
// answers 200 with the block's own outputs/statusDetail and absent:false —
// the exact shape Observation.Response() produces for Present.
func TestServe_SubscribePresentWithinBudgetAnswers200AbsentFalse(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{Outputs: map[string]string{"url": "https://example.test"}, StatusDetail: "provisioned"}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope("nonce-settle-present", nil))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Absent == nil || *resp.Absent != false {
		t.Errorf("Absent = %v, want a non-nil false", resp.Absent)
	}
	if resp.Pending {
		t.Error("Pending = true for a resource observed Present within budget")
	}
	if resp.Outputs["url"] != "https://example.test" {
		t.Errorf("Outputs[url] = %q, want the block's own output", resp.Outputs["url"])
	}
	if resp.StatusDetail != "provisioned" {
		t.Errorf("StatusDetail = %q, want the block's own detail", resp.StatusDetail)
	}
	if resp.Error != "" {
		t.Errorf("Error = %q, want empty for a successful settle", resp.Error)
	}
}

// TestServe_SubscribeBudgetExhaustedAnswersPendingNeverError pins MIT-1293:
// a resource that never reaches Present within VerbBudget attempts answers
// 200 {"pending":true}, never an error status — a block merely waiting is
// not failed.
func TestServe_SubscribeBudgetExhaustedAnswersPendingNeverError(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	absent := contract.Absent
	f := &kittest.FakeResource{ObserveState: &absent} // never reaches Present
	budget := 3
	h := newHandler(t, f, s, func(c *handler.Config) { c.VerbBudget = budget })

	status, raw := postHook(t, h, s, envelope("nonce-settle-pending", nil))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — pending is never an error (MIT-1293), body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if !resp.Pending {
		t.Error("Pending = false, want true after the budget was exhausted while never Present")
	}
	if resp.Error != "" {
		t.Errorf("Error = %q, want empty — pending must never carry an error", resp.Error)
	}
	if resp.StatusDetail == "" {
		t.Error("StatusDetail is empty, want it to name the attempts made")
	}
	if !strings.Contains(resp.StatusDetail, strconv.Itoa(budget)) {
		t.Errorf("StatusDetail = %q, want it to name the %d attempts made", resp.StatusDetail, budget)
	}
	if f.ObserveCalls != budget {
		t.Errorf("ObserveCalls = %d, want exactly VerbBudget (%d) attempts", f.ObserveCalls, budget)
	}
}

// TestServe_UpgradeBudgetExhaustedAnswersPendingNeverError mirrors the
// subscribe case for upgrade, since the CONTRACT pins the same settle
// behaviour for both.
func TestServe_UpgradeBudgetExhaustedAnswersPendingNeverError(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	unknown := contract.Unknown
	f := &kittest.FakeResource{ObserveState: &unknown}
	h := newHandler(t, f, s, func(c *handler.Config) { c.VerbBudget = 2 })

	status, raw := postHook(t, h, s, envelope("nonce-upgrade-pending", map[string]any{"operation": "upgrade"}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	resp := decodeResponse(t, raw)
	if !resp.Pending {
		t.Error("Pending = false, want true for an upgrade whose resource never settles to Present")
	}
}

// --- The settle loops stop when the caller has gone (MIT-3070 review) -------
//
// Neither poll loop checks ctx.Err(), and defaultSleep returns at once when
// the context is done, so a cancelled request fires its whole remaining
// budget back to back with no pacing, against a cloud API, for an answer
// nobody will read. The fix makes both loops break as soon as ctx.Err() is
// non-nil.

// cancelAfterNObserves cancels its own test's context from inside Observe,
// once Observe has been called cancelAfter times, and always reports
// reportState — a state that is never the target the loop is polling for,
// so the loop can only ever stop for one of two reasons: the budget ran out,
// or it noticed the context was cancelled. VerbBudget is set far larger than
// cancelAfter in every test using this fake, so a low final ObserveCalls
// count can only mean the loop honoured cancellation.
type cancelAfterNObserves struct {
	mu           sync.Mutex
	observeCalls int
	cancelAfter  int
	cancel       context.CancelFunc
	reportState  contract.State
}

func (c *cancelAfterNObserves) Ensure(context.Context, contract.Desired) error { return nil }
func (c *cancelAfterNObserves) Delete(context.Context, contract.Identity) error { return nil }

func (c *cancelAfterNObserves) Observe(context.Context, contract.Identity) (contract.Observation, error) {
	c.mu.Lock()
	c.observeCalls++
	n := c.observeCalls
	c.mu.Unlock()
	if n == c.cancelAfter {
		c.cancel()
	}
	return contract.Observation{State: c.reportState}, nil
}

func (c *cancelAfterNObserves) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observeCalls
}

// TestServe_SubscribeSettleStopsEarlyWhenContextIsCancelled pins the
// settlePresent loop: once the request's context is cancelled mid-poll, the
// handler must return well short of spending its whole VerbBudget on Observe
// calls nobody will read the answer to.
func TestServe_SubscribeSettleStopsEarlyWhenContextIsCancelled(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	const budget = 1000
	r := &cancelAfterNObserves{cancelAfter: 2, cancel: cancel, reportState: contract.Absent}
	h := newHandler(t, r, s, func(c *handler.Config) { c.VerbBudget = budget })

	done := make(chan struct{})
	go func() {
		postHookWithContext(t, h, s, envelope("nonce-settle-cancel-sub", nil), ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled — it kept spending the settle budget")
	}

	if calls := r.calls(); calls >= budget {
		t.Errorf("ObserveCalls = %d, want strictly under the %d budget — the settle loop must stop as soon as ctx is cancelled", calls, budget)
	}
}

// TestServe_UnsubscribeSettleStopsEarlyWhenContextIsCancelled mirrors the
// above for settleAbsent: the fix document is explicit that both poll loops
// need the same ctx.Err() check.
func TestServe_UnsubscribeSettleStopsEarlyWhenContextIsCancelled(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	const budget = 1000
	r := &cancelAfterNObserves{cancelAfter: 2, cancel: cancel, reportState: contract.Present}
	h := newHandler(t, r, s, func(c *handler.Config) { c.VerbBudget = budget })

	done := make(chan struct{})
	go func() {
		postHookWithContext(t, h, s, envelope("nonce-settle-cancel-unsub", map[string]any{"operation": "unsubscribe"}), ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled — it kept spending the settle budget")
	}

	if calls := r.calls(); calls >= budget {
		t.Errorf("ObserveCalls = %d, want strictly under the %d budget — the settle loop must stop as soon as ctx is cancelled", calls, budget)
	}
}
