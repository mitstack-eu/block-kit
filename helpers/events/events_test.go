package events_test

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/events"
)

var redriveCallback = flag.String("redrive-callback", "", "the callback URL a redrive's fresh process posts one heartbeat to")

func TestEmit(t *testing.T) {
	progress := func(ctx context.Context) error { return events.EmitProgress(ctx, "creating instance") }
	completed := events.EmitCompleted
	failed := func(ctx context.Context) error { return events.EmitFailed(ctx, "it broke") }
	if *redriveCallback != "" {
		if err := progress(events.WithEnvelope(context.Background(), events.Envelope{SubscriptionID: "sub-1", IdempotencyKey: "idem-1", Callback: &events.Callback{URL: *redriveCallback}})); err != nil {
			t.Fatal(err)
		}
		return
	}
	redriveFromAFreshProcess := func(ctx context.Context) error {
		env, _ := events.EnvelopeFrom(ctx)
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEmit$", "-redrive-callback="+env.Callback.URL).Run()
	}
	posted := func(status, detail, key string) map[string]any {
		return map[string]any{"subscriptionId": "sub-1", "operation": "", "status": status, "detail": detail, "idempotencyKey": key, "token": ""}
	}
	heartbeat := posted("progress", "creating instance", "idem-1")
	cases := []struct {
		name  string
		env   *events.Envelope
		emits []func(context.Context) error
		want  []map[string]any
	}{
		{"every emit is silent rather than an error when an older engine sends no callback", &events.Envelope{SubscriptionID: "sub-1"},
			[]func(context.Context) error{progress, completed, failed}, nil},
		{"an emit on a context with no envelope is silent", nil, []func(context.Context) error{progress}, nil},
		{"a progress event posts the whole envelope with its detail", &events.Envelope{SubscriptionID: "sub-1", Operation: "subscribe", IdempotencyKey: "idem-1", Callback: &events.Callback{Token: "tok-abc"}},
			[]func(context.Context) error{progress},
			[]map[string]any{{"subscriptionId": "sub-1", "operation": "subscribe", "status": "progress", "detail": "creating instance", "idempotencyKey": "idem-1", "token": "tok-abc"}}},
		{"completed and failed carry their status", &events.Envelope{SubscriptionID: "sub-1", Callback: &events.Callback{}},
			[]func(context.Context) error{completed, failed}, []map[string]any{posted("completed", "", ""), posted("failed", "it broke", "")}},
		{"a secret in the detail is scrubbed before it leaves the process", &events.Envelope{SubscriptionID: "sub-1", Callback: &events.Callback{}},
			[]func(context.Context) error{func(ctx context.Context) error {
				return events.EmitFailed(ctx, "auth failed: token=ghp_supersecretvalue while calling API")
			}}, []map[string]any{posted("failed", "auth failed: token= [REDACTED] while calling API", "")}},
		{"two identical heartbeats under one envelope post two events, the second numbered above the first", &events.Envelope{SubscriptionID: "sub-1", IdempotencyKey: "idem-1", Callback: &events.Callback{}},
			[]func(context.Context) error{progress, progress}, []map[string]any{heartbeat, heartbeat}},
		{"a redrive from a fresh process under the same key numbers its heartbeat above the first drive's", &events.Envelope{SubscriptionID: "sub-1", IdempotencyKey: "idem-1", Callback: &events.Callback{}},
			[]func(context.Context) error{progress, redriveFromAFreshProcess}, []map[string]any{heartbeat, heartbeat}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []map[string]any
			var last float64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var ev map[string]any
				_ = json.NewDecoder(r.Body).Decode(&ev)
				seq, _ := ev["sequence"].(float64)
				if seq <= last {
					t.Errorf("a %v event carries sequence %v after %.0f; want a number above it, the first at least 1", ev["status"], ev["sequence"], last)
				}
				last = seq
				delete(ev, "sequence")
				got = append(got, ev)
			}))
			ctx := context.Background()
			if tc.env != nil {
				env := *tc.env
				if env.Callback != nil {
					env.Callback = &events.Callback{URL: srv.URL, Token: env.Callback.Token}
				}
				ctx = events.WithEnvelope(ctx, env)
			}

			for i, emit := range tc.emits {
				if err := emit(ctx); err != nil {
					t.Errorf("emit #%d returned %v, want nil", i+1, err)
				}
			}
			srv.Close()

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("posted events = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestScrubDetail(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"password=hunter2", "password= [REDACTED]"},
		{"api_key: AKIAEXAMPLE", "api_key: [REDACTED]"},
		{"Secret = s3cr3t", "Secret = [REDACTED]"},
		{"private_key=-----BEGIN", "private_key= [REDACTED]"},
		{"credential:abc123", "credential: [REDACTED]"},
		{"instance i-abc123 is running", "instance i-abc123 is running"},
	} {
		if got := events.ScrubDetail(tc.in); got != tc.want {
			t.Errorf("ScrubDetail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnvelopeFrom(t *testing.T) {
	full := events.Envelope{SubscriptionID: "sub-1", Operation: "subscribe", IdempotencyKey: "key-1", Callback: &events.Callback{URL: "https://api.mitstack.dev/v1/webhooks/operator-events", Token: "evt-token"}}
	cases := []struct {
		name   string
		ctx    context.Context
		want   events.Envelope
		wantOK bool
	}{
		{"it returns the envelope WithEnvelope stashed", events.WithEnvelope(context.Background(), full), full, true},
		{"an envelope with no callback is found, with a nil callback", events.WithEnvelope(context.Background(), events.Envelope{SubscriptionID: "sub-1"}), events.Envelope{SubscriptionID: "sub-1"}, true},
		{"a context with no envelope reports not found", context.Background(), events.Envelope{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := events.EnvelopeFrom(tc.ctx)

			if ok != tc.wantOK || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("EnvelopeFrom = %+v, %v; want %+v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestReportCommit(t *testing.T) {
	const repository, commit = "acme/docker-host-sub-host0123456789", "2d266ec0449adf437f9226b938817a45da1ed95c"
	type taken struct{ path, authorization, body string }
	cases := []struct {
		name     string
		status   int
		envelope *events.Envelope
		want     []taken
		wantErr  string
	}{
		{"one report posts the repository and the commit with the callback's token as its bearer", http.StatusAccepted,
			&events.Envelope{SubscriptionID: "sub-1", Callback: &events.Callback{Token: "evt-token"}},
			[]taken{{"/v1/gitops-commits", "Bearer evt-token", `{"repository":"` + repository + `","commit":"` + commit + `"}`}}, ""},
		{"a platform answering 503 takes one request and the report is an error naming the status", http.StatusServiceUnavailable,
			&events.Envelope{SubscriptionID: "sub-1", Callback: &events.Callback{Token: "evt-token"}},
			[]taken{{"/v1/gitops-commits", "Bearer evt-token", `{"repository":"` + repository + `","commit":"` + commit + `"}`}}, "503"},
		{"a call with no events callback sends nothing and the report is an error", http.StatusAccepted,
			&events.Envelope{SubscriptionID: "sub-1"}, nil, "callback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []taken
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				got = append(got, taken{r.URL.Path, r.Header.Get("Authorization"), strings.TrimSpace(string(raw))})
				w.WriteHeader(tc.status)
			}))
			env := *tc.envelope
			if env.Callback != nil {
				env.Callback = &events.Callback{URL: srv.URL + "/v1/webhooks/operator-events", Token: env.Callback.Token}
			}

			err := events.ReportCommit(events.WithEnvelope(context.Background(), env), repository, commit)
			srv.Close()

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ReportCommit sent %v, want %v", got, tc.want)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("ReportCommit = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), "evt-token")):
				t.Errorf("ReportCommit = %v, want an error naming %q and carrying no token", err, tc.wantErr)
			}
		})
	}
}

func TestPlatformFrom(t *testing.T) {
	withURL := func(url string) context.Context {
		return events.WithEnvelope(context.Background(), events.Envelope{SubscriptionID: "sub-1", Callback: &events.Callback{URL: url, Token: "evt-token"}})
	}
	cases := []struct {
		name, wantBase string
		ctx            context.Context
	}{
		{"the callback URL without its operator-events path is the platform's base", "https://api.mitstack.dev", withURL("https://api.mitstack.dev/v1/webhooks/operator-events")},
		{"a context with no envelope is an error", "", context.Background()},
		{"an envelope with no callback is an error", "", events.WithEnvelope(context.Background(), events.Envelope{SubscriptionID: "sub-1"})},
		{"a callback URL without the operator-events path is an error", "", withURL("https://api.mitstack.dev/v1/webhooks/something-else")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, token, err := events.PlatformFrom(tc.ctx)

			if tc.wantBase == "" {
				if err == nil || strings.Contains(err.Error(), "evt-token") {
					t.Errorf("PlatformFrom = %q, token set %v, %v; want an error carrying no token", base, token != "", err)
				}
				return
			}
			if err != nil || base != tc.wantBase || token != "evt-token" {
				t.Errorf("PlatformFrom = %q, token %q, %v; want %q and the callback's token", base, token, err, tc.wantBase)
			}
		})
	}
}
