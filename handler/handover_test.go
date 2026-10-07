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

// handoverFake snapshots then destroys, and a SnapshotErr aborts before DestroyCalls is incremented, since a teardown after a failed snapshot destroys data the platform promised to carry forward; it is local because kittest.FakeResource is not extended.
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

// Delete must never be called once the Handler sees the fake implements HandoverResource.
func (f *handoverFake) Delete(ctx context.Context, id contract.Identity) error {
	f.DeleteCalls++
	f.Count = 0
	return nil
}

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

func TestServe_UnsubscribeDeleteWithRequestReceivesDataHandoverParam(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &handoverFake{Count: 1, SnapshotID: "snap-123"}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-handover-params", map[string]any{
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

func TestServe_UnsubscribeSnapshotIdLandsInOutputsBesideAbsentTrue(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &handoverFake{Count: 1, SnapshotID: "snap-456"}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-handover-outputs", map[string]any{
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

// DestroyCalls and DeleteCalls are tracked apart from SnapshotCalls so a handler that ran the plain Delete fallback, or ignored DeleteWithRequest's error and settled, is still caught.
func TestServe_UnsubscribeFailedSnapshotAbortsBeforeDestroy(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &handoverFake{Count: 1, SnapshotErr: errors.New("handover: snapshot did not complete")}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-handover-fail", map[string]any{
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

func TestServe_UnsubscribeDeleteWithRequestReceivesTheUnsubscribeSecrets(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const tokenValue = "tok-unsubscribe-delete-request-3a7d"
	f := &handoverFake{Count: 1}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-handover-secrets", map[string]any{
		"operation": "unsubscribe",
		"secrets":   map[string]any{"github": map[string]any{"token": tokenValue}},
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	assertConforms(t, raw)

	if f.DeleteWithRequestCalls != 1 {
		t.Fatalf("DeleteWithRequestCalls = %d, want 1", f.DeleteWithRequestCalls)
	}
	if got := f.LastDesired.Secrets["github"]["token"]; got != tokenValue {
		t.Errorf(`DeleteWithRequest saw Secrets["github"]["token"] = %q, want %q`, got, tokenValue)
	}
}
