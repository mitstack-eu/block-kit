// handover_test.go pins the unsubscribe route a block reaches through
// handler.HandoverResource: a block implementing it sees the unsubscribe
// request's real Desired — Params included — before it destroys anything,
// and any outputs it returns land in the unsubscribe response's Outputs
// beside "absent".
//
// kittest.FakeResource structurally mirrors handler.Resource only, so it
// does not implement HandoverResource. This file defines a small local fake
// that does, rather than extending the shared kittest fake: whether
// HandoverResource belongs on the shipped FakeResource too is a call for
// whoever owns that shared support package, not one this test file should
// make by editing it directly.
package handler_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// handoverFake is a Resource that also implements handler.HandoverResource.
// Its DeleteWithRequest models a block's own snapshot-then-destroy order:
// SnapshotErr, when set, aborts before DestroyCalls is ever incremented —
// the same order the kit's handover package doc pins ("a teardown that runs
// after a FAILED snapshot destroys data the platform has already promised
// to carry forward").
type handoverFake struct {
	Count int

	SnapshotErr error
	SnapshotID  string

	LastDesired            contract.Desired
	DeleteWithRequestCalls int
	SnapshotCalls          int
	DestroyCalls           int
	ObserveCalls           int
	DeleteCalls            int
}

func (f *handoverFake) Ensure(ctx context.Context, d contract.Desired) error { return nil }

func (f *handoverFake) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	f.ObserveCalls++
	state := contract.Absent
	if f.Count > 0 {
		state = contract.Present
	}
	return contract.Observation{State: state}, nil
}

// Delete is the plain path handler.Resource still requires. It must never be
// called for this fake once the Handler sees it also implements
// HandoverResource — DeleteCalls proves that in
// TestServe_UnsubscribeFailedSnapshotAbortsBeforeDestroy.
func (f *handoverFake) Delete(ctx context.Context, id contract.Identity) error {
	f.DeleteCalls++
	f.Count = 0
	return nil
}

// DeleteWithRequest is handler.HandoverResource's method: it receives the
// unsubscribe's real Desired, snapshots when dataHandover asks for one, and
// only then destroys.
func (f *handoverFake) DeleteWithRequest(ctx context.Context, d contract.Desired) (map[string]string, error) {
	f.DeleteWithRequestCalls++
	f.LastDesired = d

	if d.Params["dataHandover"] == "snapshot" {
		f.SnapshotCalls++
		if f.SnapshotErr != nil {
			return nil, f.SnapshotErr
		}
		f.DestroyCalls++
		f.Count = 0
		if f.SnapshotID == "" {
			return nil, nil
		}
		return map[string]string{"dataSnapshotId": f.SnapshotID}, nil
	}

	f.DestroyCalls++
	f.Count = 0
	return nil, nil
}

var _ handler.Resource = (*handoverFake)(nil)
var _ handler.HandoverResource = (*handoverFake)(nil)

// TestServe_UnsubscribeDeleteWithRequestReceivesDataHandoverParam pins the
// first acceptance criterion: a block on HandoverResource sees the
// unsubscribe request's real Params, dataHandover included, before it
// destroys anything. Today's kit builds the delete hooks' Desired from the
// bare Identity (dispatch.go ~137) and calls only Delete(ctx, Identity), so
// this is red until the kit both grows HandoverResource and threads
// req.Desired() through to it.
func TestServe_UnsubscribeDeleteWithRequestReceivesDataHandoverParam(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &handoverFake{Count: 1, SnapshotID: "snap-123"}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope("nonce-handover-params", map[string]any{
		"operation": "unsubscribe",
		"params":    map[string]string{"dataHandover": "snapshot"},
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	if f.DeleteWithRequestCalls != 1 {
		t.Fatalf("DeleteWithRequestCalls = %d, want 1", f.DeleteWithRequestCalls)
	}
	if got := f.LastDesired.Params["dataHandover"]; got != "snapshot" {
		t.Errorf("DeleteWithRequest saw Params[dataHandover] = %q, want %q", got, "snapshot")
	}
	if f.SnapshotCalls != 1 {
		t.Errorf("SnapshotCalls = %d, want 1 — the param must have driven the snapshot step", f.SnapshotCalls)
	}
}

// TestServe_UnsubscribeSnapshotIdLandsInOutputsBesideAbsentTrue pins the
// second acceptance criterion: the snapshot id DeleteWithRequest returns
// under "dataSnapshotId" (handover.OutputKey) reaches the unsubscribe
// response's outputs, alongside absent:true once Observe confirms the
// resource is gone. Today's absent response (dispatch.go ~210) carries only
// what Observe produced, so this is red until the delete path's outputs are
// merged in.
func TestServe_UnsubscribeSnapshotIdLandsInOutputsBesideAbsentTrue(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &handoverFake{Count: 1, SnapshotID: "snap-456"}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope("nonce-handover-outputs", map[string]any{
		"operation": "unsubscribe",
		"params":    map[string]string{"dataHandover": "snapshot"},
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Absent == nil || *resp.Absent != true {
		t.Fatalf("Absent = %v, want a non-nil true", resp.Absent)
	}
	if got := resp.Outputs["dataSnapshotId"]; got != "snap-456" {
		t.Errorf(`Outputs["dataSnapshotId"] = %q, want %q`, got, "snap-456")
	}
}

// TestServe_UnsubscribeFailedSnapshotAbortsBeforeDestroy pins the third
// acceptance criterion: a snapshot step that fails answers an error and
// never reaches the destroy step. DestroyCalls and DeleteCalls are tracked
// separately from SnapshotCalls precisely so a handler that ran the plain
// Delete fallback anyway, or that ignored DeleteWithRequest's error and
// settled regardless, is still caught.
func TestServe_UnsubscribeFailedSnapshotAbortsBeforeDestroy(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &handoverFake{Count: 1, SnapshotErr: errors.New("handover: snapshot did not complete")}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope("nonce-handover-fail", map[string]any{
		"operation": "unsubscribe",
		"params":    map[string]string{"dataHandover": "snapshot"},
	}))
	if status == http.StatusOK {
		t.Fatalf("status = 200, want an error status, body: %s", raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Error == "" {
		t.Error("Error is empty, want the snapshot failure surfaced as an error")
	}
	if f.DestroyCalls != 0 {
		t.Errorf("DestroyCalls = %d, want 0 — a failed snapshot must abort before anything is destroyed", f.DestroyCalls)
	}
	if f.DeleteCalls != 0 {
		t.Errorf("DeleteCalls = %d, want 0 — the plain Delete fallback must never run once HandoverResource handled the request", f.DeleteCalls)
	}
	if f.ObserveCalls != 0 {
		t.Errorf("ObserveCalls = %d, want 0 — settle must never run after an aborted delete", f.ObserveCalls)
	}
}
