package handler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mitstack-eu/operator-kit/kittest"
)

// recordedEvent mirrors the operatorEvent JSON shape the events package
// POSTs (MIT-1151), decoded independently here so this test proves the
// wire shape rather than importing the events package's own type.
type recordedEvent struct {
	SubscriptionID string `json:"subscriptionId"`
	Token          string `json:"token"`
	Operation      string `json:"operation"`
	Status         string `json:"status"`
	Detail         string `json:"detail"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// fakeEventsServer records every event POSTed to it.
type fakeEventsServer struct {
	*httptest.Server
	mu     sync.Mutex
	events []recordedEvent
}

func newFakeEventsServer(t *testing.T) *fakeEventsServer {
	t.Helper()
	f := &fakeEventsServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev recordedEvent
		_ = json.NewDecoder(r.Body).Decode(&ev)
		f.mu.Lock()
		f.events = append(f.events, ev)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeEventsServer) Events() []recordedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedEvent, len(f.events))
	copy(out, f.events)
	return out
}

func eventsCallbackField(url, token string) map[string]any {
	return map[string]any{"url": url, "token": token}
}

// TestServe_EmitsProgressThenCompletedOnSuccess pins that a successful verb
// emits a progress event when it starts and a completed event on its 2xx
// outcome.
func TestServe_EmitsProgressThenCompletedOnSuccess(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	h := newHandler(t, f, s)
	events := newFakeEventsServer(t)

	body := envelope("nonce-events-ok", map[string]any{
		"eventsCallback": eventsCallbackField(events.URL, "events-tok"),
	})
	status, raw := postHook(t, h, s, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}

	got := events.Events()
	if len(got) < 2 {
		t.Fatalf("recorded %d events, want at least progress+completed: %+v", len(got), got)
	}
	if got[0].Status != "progress" {
		t.Errorf("first event status = %q, want %q", got[0].Status, "progress")
	}
	if last := got[len(got)-1]; last.Status != "completed" {
		t.Errorf("last event status = %q, want %q", last.Status, "completed")
	}
	for _, ev := range got {
		if ev.SubscriptionID != "sub-1" {
			t.Errorf("event SubscriptionID = %q, want %q", ev.SubscriptionID, "sub-1")
		}
		if ev.Token != "events-tok" {
			t.Errorf("event Token = %q, want the eventsCallback's own token", ev.Token)
		}
	}
}

// TestServe_EmitsFailedWithTheFailureDetailOnError pins the failure half:
// EmitFailed carries the failure detail on a 4xx/5xx outcome.
func TestServe_EmitsFailedWithTheFailureDetailOnError(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{EnsureErr: errors.New("resource backend unavailable")}
	h := newHandler(t, f, s)
	events := newFakeEventsServer(t)

	body := envelope("nonce-events-fail", map[string]any{
		"eventsCallback": eventsCallbackField(events.URL, "events-tok"),
	})
	status, raw := postHook(t, h, s, body)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body: %s", status, raw)
	}

	got := events.Events()
	if len(got) == 0 {
		t.Fatal("no events recorded, want at least a failed event")
	}
	last := got[len(got)-1]
	if last.Status != "failed" {
		t.Errorf("last event status = %q, want %q", last.Status, "failed")
	}
	if !strings.Contains(last.Detail, "resource backend unavailable") {
		t.Errorf("failed event detail = %q, want it to carry the failure detail", last.Detail)
	}
}

// TestServe_EventsFailureNeverChangesTheLifecycleResponse pins ADR 0042's
// advisory rule: an events endpoint returning 500, or one that cannot be
// reached at all, must not change the lifecycle outcome.
func TestServe_EventsFailureNeverChangesTheLifecycleResponse(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"events endpoint returns 500", ""}, // filled below with a real 500 server
		{"events endpoint unreachable", "http://127.0.0.1:1/events"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := kittest.NewSigner(t, "kid-1", 7)
			f := &kittest.FakeResource{}
			h := newHandler(t, f, s)

			url := c.url
			if url == "" {
				failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				t.Cleanup(failing.Close)
				url = failing.URL
			}

			body := envelope("nonce-events-advisory-"+c.name, map[string]any{
				"eventsCallback": eventsCallbackField(url, "events-tok"),
			})
			status, raw := postHook(t, h, s, body)
			if status != http.StatusOK {
				t.Errorf("status = %d, want 200 — a broken events channel must never change the lifecycle response, body: %s", status, raw)
			}
			resp := decodeResponse(t, raw)
			if resp.Error != "" {
				t.Errorf("Error = %q, want empty — the events channel is advisory only", resp.Error)
			}
		})
	}
}
