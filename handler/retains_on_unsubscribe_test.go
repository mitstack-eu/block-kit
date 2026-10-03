// retains_on_unsubscribe_test.go pins the spec.lifecycle.retainsOnUnsubscribe
// manifest declaration: a block whose manifest declares
// spec.lifecycle.retainsOnUnsubscribe: true answers unsubscribe with
// absent:true as soon as Delete (or DeleteWithRequest) and the afterDelete
// hook have succeeded, carrying any handover outputs, and never polls
// Observe. A block that does not declare it — or declares it false — keeps
// today's behaviour exactly: settleAbsent polls Observe up to VerbBudget and
// answers absent:false when the budget runs out still Present.
//
// Every test here drives handler.New and the served HTTP handler only, the
// same way unsubscribe_test.go and newconfig_test.go do; none of them reach
// into an unexported symbol.
package handler_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// manifestWithRetains builds a minimal block manifest whose
// spec.lifecycle carries retainsOnUnsubscribe: <value> verbatim — so a test
// can hand it a non-boolean value too — at the same indentation
// newconfig_test.go's manifestDeclaring uses for spec.lifecycle.hooks, with
// hooks optionally declared alongside it so a registered hook stays
// declared.
func manifestWithRetains(value string, hooks ...string) []byte {
	var b strings.Builder
	b.WriteString("apiVersion: mitstack/v1alpha\n")
	b.WriteString("kind: Block\n")
	b.WriteString("metadata:\n  id: test-block\n  name: Test Block\n")
	b.WriteString("spec:\n  layer: infra\n  lifecycle:\n")
	b.WriteString("    retainsOnUnsubscribe: " + value + "\n")
	if len(hooks) > 0 {
		b.WriteString("    hooks:\n")
		for _, h := range hooks {
			b.WriteString("      - " + h + "\n")
		}
	}
	return []byte(b.String())
}

// manifestWithDecoyTopLevelRetains carries a top-level retainsOnUnsubscribe
// key, a decoy outside spec.lifecycle, ahead of a spec.lifecycle that
// declares no retainsOnUnsubscribe of its own. No manifest field is named
// retainsOnUnsubscribe at the top level.
const manifestWithDecoyTopLevelRetains = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: test-block
  name: Test Block
retainsOnUnsubscribe: true
spec:
  layer: infra
  lifecycle:
    hooks: []
`

// retainsHandoverFake is a Resource that also implements
// handler.HandoverResource, modelling a backend whose Observe keeps
// reporting Present indefinitely even after DeleteWithRequest has already
// succeeded — the exact eventually-consistent case retainsOnUnsubscribe
// exists to skip past. Delete errors if ever called, since a declared
// HandoverResource must never fall back to it.
type retainsHandoverFake struct {
	Outputs map[string]string

	DeleteWithRequestCalls int
	ObserveCalls           int
}

func (f *retainsHandoverFake) Ensure(ctx context.Context, d contract.Desired) error { return nil }

func (f *retainsHandoverFake) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	f.ObserveCalls++
	return contract.Observation{State: contract.Present}, nil
}

func (f *retainsHandoverFake) Delete(ctx context.Context, id contract.Identity) error {
	return errors.New("retainsHandoverFake: Delete must never be called once HandoverResource is implemented")
}

func (f *retainsHandoverFake) DeleteWithRequest(ctx context.Context, d contract.Desired) (map[string]string, error) {
	f.DeleteWithRequestCalls++
	return f.Outputs, nil
}

var _ handler.Resource = (*retainsHandoverFake)(nil)
var _ handler.HandoverResource = (*retainsHandoverFake)(nil)

// --- Criterion 1: the declared short-circuit itself ------------------------

// TestServe_UnsubscribeAbsentTrueAfterOneDeleteZeroObserveWhenManifestDeclaresRetainsOnUnsubscribe
// pins the core behaviour: with spec.lifecycle.retainsOnUnsubscribe: true
// declared, a Resource whose Observe keeps answering Present forever still
// gets absent:true after exactly one Delete call and zero Observe calls.
func TestServe_UnsubscribeAbsentTrueAfterOneDeleteZeroObserveWhenManifestDeclaresRetainsOnUnsubscribe(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	present := contract.Present
	f := &kittest.FakeResource{Count: 1, ObserveState: &present} // Observe keeps answering Present however Delete leaves Count
	h := newHandler(t, f, s, func(c *handler.Config) {
		c.Manifest = manifestWithRetains("true")
		c.VerbBudget = 5
	})

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-retains-absent-true", map[string]any{"operation": "unsubscribe"}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Absent == nil || *resp.Absent != true {
		t.Errorf("Absent = %v, want a non-nil true", resp.Absent)
	}
	if f.DeleteCalls != 1 {
		t.Errorf("DeleteCalls = %d, want exactly 1", f.DeleteCalls)
	}
	if f.ObserveCalls != 0 {
		t.Errorf("ObserveCalls = %d, want 0 — a declared retainsOnUnsubscribe handler must never poll Observe on unsubscribe", f.ObserveCalls)
	}
}

// --- Criterion 2: the default, and an explicit false, are unchanged --------

// TestServe_UnsubscribeAbsentFalseAfterFullBudgetWhenManifestDoesNotDeclareRetainsOnUnsubscribe
// pins that with no manifest declaration at all, unsubscribe keeps polling
// Observe to the full VerbBudget and answers absent:false — today's
// behaviour, unchanged. This is expected to pass against the tree as it
// stands today: it is the control the declared cases are measured against.
func TestServe_UnsubscribeAbsentFalseAfterFullBudgetWhenManifestDoesNotDeclareRetainsOnUnsubscribe(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	present := contract.Present
	f := &kittest.FakeResource{Count: 1, ObserveState: &present}
	h := newHandler(t, f, s, func(c *handler.Config) { c.VerbBudget = 3 }) // no Manifest at all

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-retains-default-absent-false", map[string]any{"operation": "unsubscribe"}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Absent == nil || *resp.Absent != false {
		t.Errorf("Absent = %v, want a non-nil false — the default is unchanged", resp.Absent)
	}
	if f.ObserveCalls != 3 {
		t.Errorf("ObserveCalls = %d, want exactly the VerbBudget (3)", f.ObserveCalls)
	}
}

// TestServe_UnsubscribeAbsentFalseAfterFullBudgetWhenManifestDeclaresRetainsOnUnsubscribeFalse
// mirrors the control above for an explicit retainsOnUnsubscribe: false,
// which must behave exactly like no declaration at all.
func TestServe_UnsubscribeAbsentFalseAfterFullBudgetWhenManifestDeclaresRetainsOnUnsubscribeFalse(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	present := contract.Present
	f := &kittest.FakeResource{Count: 1, ObserveState: &present}
	h := newHandler(t, f, s, func(c *handler.Config) {
		c.Manifest = manifestWithRetains("false")
		c.VerbBudget = 3
	})

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-retains-explicit-false", map[string]any{"operation": "unsubscribe"}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Absent == nil || *resp.Absent != false {
		t.Errorf("Absent = %v, want a non-nil false — an explicit retainsOnUnsubscribe: false behaves like no declaration", resp.Absent)
	}
	if f.ObserveCalls != 3 {
		t.Errorf("ObserveCalls = %d, want exactly the VerbBudget (3)", f.ObserveCalls)
	}
}

// --- Criterion 3: a failed Delete or afterDelete hook never claims absent --

// TestServe_UnsubscribeFailedDeleteAnswersErrorNotAbsentTrueWhenRetainsOnUnsubscribeDeclared
// pins that a declared handler whose Delete fails still answers the
// ordinary error response, never absent:true, and never touches Observe.
func TestServe_UnsubscribeFailedDeleteAnswersErrorNotAbsentTrueWhenRetainsOnUnsubscribeDeclared(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{Count: 1, DeleteErr: errors.New("delete backend unavailable")}
	h := newHandler(t, f, s, func(c *handler.Config) {
		c.Manifest = manifestWithRetains("true")
	})

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-retains-delete-err", map[string]any{"operation": "unsubscribe"}))
	if status == http.StatusOK {
		t.Fatalf("status = 200, want an error status, body: %s", raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Error == "" {
		t.Error("Error is empty, want the Delete failure surfaced as an error")
	}
	if resp.Absent != nil && *resp.Absent == true {
		t.Error("Absent = true, want no absent:true — a failed Delete must never be answered as absent, declaration or not")
	}
	if f.ObserveCalls != 0 {
		t.Errorf("ObserveCalls = %d, want 0 — settle must never run after a failed Delete", f.ObserveCalls)
	}
}

// TestServe_UnsubscribeFailedAfterDeleteHookAnswersErrorNotAbsentTrueWhenRetainsOnUnsubscribeDeclared
// mirrors the above for a failing afterDelete hook: Delete itself succeeds,
// but the declared afterDelete hook errors, so the response must still be
// the ordinary error response, never absent:true, and Observe must still
// never run.
func TestServe_UnsubscribeFailedAfterDeleteHookAnswersErrorNotAbsentTrueWhenRetainsOnUnsubscribeDeclared(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{Count: 1}
	hookErr := errors.New("afterDelete hook failed")
	h := newHandler(t, f, s, func(c *handler.Config) {
		c.Manifest = manifestWithRetains("true", "afterDelete")
		c.Hooks = map[string]handler.Hook{
			handler.HookAfterDelete: func(ctx context.Context, d contract.Desired) error { return hookErr },
		}
	})

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-retains-afterdelete-err", map[string]any{"operation": "unsubscribe"}))
	if status == http.StatusOK {
		t.Fatalf("status = 200, want an error status, body: %s", raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Absent != nil && *resp.Absent == true {
		t.Error("Absent = true, want no absent:true — a failed afterDelete hook must never be answered as absent")
	}
	if f.DeleteCalls != 1 {
		t.Errorf("DeleteCalls = %d, want 1 — Delete itself must have run before the failing hook", f.DeleteCalls)
	}
	if f.ObserveCalls != 0 {
		t.Errorf("ObserveCalls = %d, want 0 — settle must never run after a failed afterDelete hook", f.ObserveCalls)
	}
}

// --- Criterion 4: a HandoverResource carries its outputs through -----------

// TestServe_UnsubscribeHandoverOutputsLandInAbsentTrueWithZeroObserveWhenRetainsOnUnsubscribeDeclared
// pins that a declared handler whose Resource also implements
// HandoverResource answers absent:true carrying the outputs
// DeleteWithRequest returned, still with zero Observe calls.
func TestServe_UnsubscribeHandoverOutputsLandInAbsentTrueWithZeroObserveWhenRetainsOnUnsubscribeDeclared(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &retainsHandoverFake{Outputs: map[string]string{"dataSnapshotId": "snap-789"}}
	h := newHandler(t, f, s, func(c *handler.Config) {
		c.Manifest = manifestWithRetains("true")
	})

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-retains-handover", map[string]any{"operation": "unsubscribe"}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Absent == nil || *resp.Absent != true {
		t.Fatalf("Absent = %v, want a non-nil true", resp.Absent)
	}
	if got := resp.Outputs["dataSnapshotId"]; got != "snap-789" {
		t.Errorf(`Outputs["dataSnapshotId"] = %q, want %q`, got, "snap-789")
	}
	if f.DeleteWithRequestCalls != 1 {
		t.Errorf("DeleteWithRequestCalls = %d, want 1", f.DeleteWithRequestCalls)
	}
	if f.ObserveCalls != 0 {
		t.Errorf("ObserveCalls = %d, want 0 — a declared retainsOnUnsubscribe handler must never poll Observe, HandoverResource included", f.ObserveCalls)
	}
}

// --- Criterion 5: New reads and validates the declaration -------------------

// TestNew_ErrorsWhenRetainsOnUnsubscribeValueIsNotABoolean pins that New
// refuses a manifest whose spec.lifecycle.retainsOnUnsubscribe is neither
// true nor false, naming the field, the same way it already refuses an
// unreadable spec.lifecycle.hooks value.
func TestNew_ErrorsWhenRetainsOnUnsubscribeValueIsNotABoolean(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = manifestWithRetains("maybe")

	_, err := handler.New(cfg)
	if err == nil {
		t.Fatal("expected an error for a retainsOnUnsubscribe value that is neither true nor false, got none")
	}
	if !strings.Contains(err.Error(), "retainsOnUnsubscribe") {
		t.Errorf("error %q does not name the field retainsOnUnsubscribe", err.Error())
	}
	requireNewRefusesWithTheParsersMessage(t, cfg, "spec.lifecycle.retainsOnUnsubscribe")
}

// TestNew_DecoyTopLevelRetainsOnUnsubscribeIsRefusedAsUnknown pins that a
// retainsOnUnsubscribe: key outside spec.lifecycle, a decoy at the
// document's top level, is a key no manifest field names, so New refuses
// the manifest with the parser's message rather than reading the decoy or
// passing over it.
func TestNew_DecoyTopLevelRetainsOnUnsubscribeIsRefusedAsUnknown(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	cfg := baseConfig(t, &kittest.FakeResource{}, s)
	cfg.Manifest = []byte(manifestWithDecoyTopLevelRetains)

	requireNewRefusesWithTheParsersMessage(t, cfg, "retainsOnUnsubscribe")
}

// --- Criterion 6: validate is unaffected by the declaration ----------------

// TestServe_ValidateStillCallsObserveOnceWhenRetainsOnUnsubscribeDeclared
// pins that validate on a declared handler still calls Observe exactly
// once, unpolled, the same as any other handler — the declaration only
// changes unsubscribe.
func TestServe_ValidateStillCallsObserveOnceWhenRetainsOnUnsubscribeDeclared(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{Count: 1}
	h := newHandler(t, f, s, func(c *handler.Config) {
		c.Manifest = manifestWithRetains("true")
	})

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-retains-validate", map[string]any{"operation": "validate"}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	if f.ObserveCalls != 1 {
		t.Errorf("ObserveCalls = %d, want exactly 1 — validate is unaffected by retainsOnUnsubscribe", f.ObserveCalls)
	}
	if f.DeleteCalls != 0 {
		t.Errorf("DeleteCalls = %d, want 0 — validate never deletes", f.DeleteCalls)
	}
}
