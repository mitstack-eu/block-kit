// A block whose manifest lists logs answers a signed POST /logs with the lines its logs function reads for that subscription's request.
package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/events"
	"github.com/mitstack-eu/block-kit/verify/kittest"
	"github.com/mitstack-eu/block-kit/webhook"
)

type logsCall struct {
	d                   contract.Desired
	platform, token     string
	platformUnreachable error
}

// logsReader records each request its logs function is handed, with the platform route the request's events callback opens.
type logsReader struct {
	mu    sync.Mutex
	calls []logsCall
	lines []string
	err   error
}

func (l *logsReader) read(ctx context.Context, d contract.Desired) ([]string, error) {
	platform, token, err := events.PlatformFrom(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, logsCall{d: d, platform: platform, token: token, platformUnreachable: err})
	return l.lines, l.err
}

func (l *logsReader) called() []logsCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

const listedVerbs = "operations: [subscribe, unsubscribe, upgrade, validate]"

// servesLogs gives the harness the plain golden manifest listing logs, as a block a user builds on the kit declares it, and names l's logs function.
func servesLogs(t *testing.T, l *logsReader) func(*handler.Config) {
	manifest := withManifestEdit(t, plainV1Beta(t), listedVerbs, "operations: [subscribe, unsubscribe, upgrade, validate, logs]")
	return then(servesBlock(manifest), func(c *handler.Config) { c.SubscriptionLogs = l.read })
}

func logsFields(t *testing.T, s *kittest.Signer, extra map[string]any) map[string]any {
	fields := plainRequest(t, s, webhook.OBOClaims{Audience: "plain", OrgID: "org-1", AppID: "app-1", SubscriptionID: "sub-1"})
	fields["operation"] = "logs"
	for k, v := range extra {
		fields[k] = v
	}
	return fields
}

func (x *harness) logsPost(fields map[string]any) *http.Request {
	x.t.Helper()
	return x.s.HookRequest(x.t, "/logs", x.envelope(fields))
}

func decodeLines(t *testing.T, body []byte) []string {
	t.Helper()
	var doc struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the answer is not {\"lines\": [...]}: %v; body: %s", err, body)
	}
	return doc.Lines
}

func TestServeLogs_HandsTheSubscriptionsRequestToTheLogsFunction(t *testing.T) {
	l := &logsReader{lines: []string{"app: listening on :8080"}}
	x := newHarness(t, &kittest.FakeResource{}, servesLogs(t, l))
	host := map[string]string{"subscriptionId": "sub-host", "address": "203.0.113.10"}

	status, body := x.serve(x.logsPost(logsFields(t, x.s, map[string]any{
		"bindings":       map[string]any{"docker-host": host},
		"bindingsAll":    map[string]any{"docker-host": []any{host}},
		"eventsCallback": map[string]any{"url": "https://api.example/v1/webhooks/operator-events", "token": "callback-token"},
	})))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	if got := decodeLines(t, body); !slices.Equal(got, l.lines) {
		t.Errorf("lines = %v, want what the logs function read, %v", got, l.lines)
	}
	calls := l.called()
	if len(calls) != 1 {
		t.Fatalf("the logs function ran %d times, want once", len(calls))
	}
	got := calls[0]
	if got.d.SubscriptionID != "sub-1" || got.d.Bindings["docker-host"]["subscriptionId"] != "sub-host" || len(got.d.BindingsAll["docker-host"]) != 1 {
		t.Errorf("the logs function got subscription %q, bindings %v and bindingsAll %v; want sub-1 and the request's docker-host binding",
			got.d.SubscriptionID, got.d.Bindings, got.d.BindingsAll)
	}
	if got.platformUnreachable != nil || got.platform != "https://api.example" || got.token != "callback-token" {
		t.Errorf("the logs function reaches the platform at %q with token %q (error %v), want the request's events callback",
			got.platform, got.token, got.platformUnreachable)
	}
}

func TestServeLogs_RefusesAnUntrustedRequestBeforeTheLogsFunction(t *testing.T) {
	type refusal struct {
		name    string
		request func(x *harness) *http.Request
		status  int
	}
	cases := []refusal{
		{"an unsigned POST is a 401", func(x *harness) *http.Request {
			r := x.logsPost(logsFields(t, x.s, nil))
			r.Header.Del("X-Mitstack-Signature-Ed25519")
			return r
		}, http.StatusUnauthorized},
		{"a forged signature is a 401", func(x *harness) *http.Request {
			body := x.envelope(logsFields(t, x.s, nil))
			r := x.s.HookRequest(t, "/logs", body)
			r.Header.Set("X-Mitstack-Signature-Ed25519", kittest.NewSigner(t, "kid-1", 99).SignBody(body))
			return r
		}, http.StatusUnauthorized},
		{"a stale createdAt is a 401", func(x *harness) *http.Request {
			body := kittest.Envelope("nonce-stale-logs", time.Now().Add(-webhook.ReplaySkew-time.Minute), logsFields(t, x.s, nil))
			return x.s.HookRequest(t, "/logs", body)
		}, http.StatusUnauthorized},
		{"a token minted for another subscription is a 401", func(x *harness) *http.Request {
			other := mintOBO(t, x.s, webhook.OBOClaims{Audience: "plain", OrgID: "org-1", AppID: "app-1", SubscriptionID: "sub-2"})
			return x.logsPost(logsFields(t, x.s, map[string]any{"obo": other}))
		}, http.StatusUnauthorized},
		{"a request with no obo field is a 401", func(x *harness) *http.Request {
			fields := logsFields(t, x.s, nil)
			delete(fields, "obo")
			return x.s.HookRequest(t, "/logs", kittest.Envelope("nonce-no-obo-logs", time.Now(), fields))
		}, http.StatusUnauthorized},
		{"a lifecycle verb sent to /logs is a 400", func(x *harness) *http.Request {
			return x.logsPost(logsFields(t, x.s, map[string]any{"operation": "subscribe"}))
		}, http.StatusBadRequest},
		{"a GET with a body-signed request's headers is a 401", func(x *harness) *http.Request {
			r := x.logsPost(logsFields(t, x.s, nil))
			r.Method = http.MethodGet
			return r
		}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &logsReader{lines: []string{"must not leave the block"}}
			f := &kittest.FakeResource{}
			x := newHarness(t, f, servesLogs(t, l), func(c *handler.Config) { c.Logs = webhook.NewLogBuffer(10) })

			status, body := x.serve(tc.request(x))

			if status != tc.status {
				t.Errorf("status = %d, want %d; body: %s", status, tc.status, body)
			}
			if resp := decodeResponse(t, body); handler.RefusedClass(resp.Error) == "" {
				t.Errorf("a %d is a refusal, so its error must carry its class; body: %s", status, body)
			}
			if n := len(l.called()); n != 0 {
				t.Errorf("the logs function ran %d times, want none", n)
			}
			assertCalls(t, f, [3]int{})
		})
	}
}

func TestServeLogs_AReplayedRequestIsRefusedAndReadsNoLines(t *testing.T) {
	l := &logsReader{lines: []string{"one line"}}
	x := newHarness(t, &kittest.FakeResource{}, servesLogs(t, l))
	body := x.envelope(logsFields(t, x.s, nil))

	first, _ := x.serve(x.s.HookRequest(t, "/logs", body))
	replay, _ := x.serve(x.s.HookRequest(t, "/logs", body))

	if first != http.StatusOK || replay != http.StatusUnauthorized {
		t.Errorf("first and replayed status = %d, %d; want 200, 401", first, replay)
	}
	if n := len(l.called()); n != 1 {
		t.Errorf("the logs function ran %d times, want once: the replay never reaches it", n)
	}
}

func TestServeLogs_TheSignedGETStillAnswersFromTheBuffer(t *testing.T) {
	l := &logsReader{lines: []string{"from the logs function"}}
	x := newHarness(t, &kittest.FakeResource{}, servesLogs(t, l), func(c *handler.Config) {
		c.Logs = webhook.NewLogBuffer(10)
		c.Logs.Append("from the operator buffer")
	})

	status, body := x.serve(x.s.LogsRequest(t, "/logs", time.Now()))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	if got := decodeLines(t, body); !slices.Equal(got, []string{"from the operator buffer"}) {
		t.Errorf("lines = %v, want the buffer's line", got)
	}
	if n := len(l.called()); n != 0 {
		t.Errorf("the logs function ran %d times for a GET, want none", n)
	}
}

func TestServeLogs_ScrubsTheRequestsSecretsFromTheLines(t *testing.T) {
	const secret = "hcloud-token-s3cret"
	l := &logsReader{lines: []string{"connecting with " + secret}}
	x := newHarness(t, &kittest.FakeResource{}, servesLogs(t, l))

	status, body := x.serve(x.logsPost(logsFields(t, x.s, map[string]any{"secrets": map[string]any{"hetzner": map[string]string{"token": secret}}})))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	kittest.AssertNoSecret(t, "the logs answer", string(body), secret)
	if got := decodeLines(t, body); len(got) != 1 || !strings.HasPrefix(got[0], "connecting with ") {
		t.Errorf("lines = %v, want the one line with its secret scrubbed", got)
	}
}

func TestServeLogs_ALogsFunctionRefusalIsA400WithItsClass(t *testing.T) {
	l := &logsReader{err: handler.Refuse("no-container", "the app has no container on the host yet")}
	x := newHarness(t, &kittest.FakeResource{}, servesLogs(t, l))

	status, body := x.serve(x.logsPost(logsFields(t, x.s, nil)))

	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
	if got := handler.RefusedClass(decodeResponse(t, body).StatusDetail); got != "no-container" {
		t.Errorf("refusal class = %q, want no-container; body: %s", got, body)
	}
}

func TestServeHook_RefusesALogsRequest(t *testing.T) {
	l := &logsReader{lines: []string{"must not leave the block"}}
	f := &kittest.FakeResource{}
	x := newHarness(t, f, servesLogs(t, l))

	status, body := x.serve(x.s.HookRequest(t, "/hook", x.envelope(logsFields(t, x.s, nil))))

	if status != http.StatusBadRequest || handler.RefusedClass(decodeResponse(t, body).Error) == "" {
		t.Errorf("status = %d, want a 400 refusal with a class, since a logs request belongs on /logs; body: %s", status, body)
	}
	if n := len(l.called()); n != 0 {
		t.Errorf("the logs function ran %d times, want none", n)
	}
	assertCalls(t, f, [3]int{})
}

func TestNew_RefusesALogsDeclarationWithoutItsFunction(t *testing.T) {
	listsLogs := withManifestEdit(t, plainV1Beta(t), listedVerbs, "operations: [subscribe, unsubscribe, upgrade, validate, logs]")
	l := &logsReader{}
	cases := []struct {
		name   string
		config func(*handler.Config)
		naming string
	}{
		{"a manifest listing logs with no logs function", servesBlock(listsLogs), "Config.SubscriptionLogs"},
		{"a logs function on a manifest not listing logs", then(servesBlock(plainV1Beta(t)), func(c *handler.Config) { c.SubscriptionLogs = l.read }), "logs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := kittest.NewSigner(t, "kid-1", 7)
			cfg := baseConfig(t, &kittest.FakeResource{}, s)
			tc.config(&cfg)

			_, err := handler.New(cfg)

			if err == nil || !strings.Contains(err.Error(), tc.naming) {
				t.Errorf("handler.New error = %v, want a refusal naming %q", err, tc.naming)
			}
		})
	}
}
