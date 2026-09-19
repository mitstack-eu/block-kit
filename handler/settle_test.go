package handler_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

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
