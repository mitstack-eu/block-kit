// A block whose manifest lists logs answers a signed POST /logs with the lines its logs function reads for that subscription's request.
package handler_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/events"
	"github.com/mitstack-eu/block-kit/verify/kittest"
	"github.com/mitstack-eu/block-kit/webhook"
)

// logsReader records each request its logs function is handed, with the platform route the request's events callback opens.
type logsReader struct {
	calls           []contract.Desired
	platform, token string
	lines           []string
	err             error
}

func (l *logsReader) read(ctx context.Context, d contract.Desired) ([]string, error) {
	l.calls = append(l.calls, d)
	l.platform, l.token, _ = events.PlatformFrom(ctx)
	return l.lines, l.err
}

// servesLogs gives the harness the plain golden manifest listing logs, as a block a user builds on the kit declares it, and names l's logs function.
func servesLogs(t *testing.T, l *logsReader) func(*handler.Config) {
	return then(servesBlock(listsLogs(t)), func(c *handler.Config) { c.SubscriptionLogs = l.read })
}

func listsLogs(t *testing.T) []byte {
	return withManifestEdit(t, plainV1Beta(t), "validate]", "validate, logs]")
}

// logsBody is a logs request for sub-1 with extra laid over its fields.
func (x *harness) logsBody(extra map[string]any) []byte {
	x.t.Helper()
	fields := plainRequest(x.t, x.s, webhook.OBOClaims{Audience: "plain", OrgID: "org-1", AppID: "app-1", SubscriptionID: "sub-1"})
	fields["operation"] = "logs"
	for k, v := range extra {
		fields[k] = v
	}
	return x.envelope(fields)
}

func (x *harness) postLogs(extra map[string]any) (int, []byte) {
	x.t.Helper()
	return x.serve(x.s.HookRequest(x.t, "/logs", x.logsBody(extra)))
}

func TestServeLogs_HandsThePOSTsRequestToTheLogsFunctionAndAnswersTheGETFromTheBuffer(t *testing.T) {
	l := &logsReader{lines: []string{"app: listening on :8080"}}
	x := newHarness(t, &kittest.FakeResource{}, servesLogs(t, l), func(c *handler.Config) {
		c.Logs = webhook.NewLogBuffer(10)
		c.Logs.Append("from the operator buffer")
	})

	status, body := x.postLogs(map[string]any{
		"bindings":       map[string]any{"docker-host": map[string]string{"subscriptionId": "sub-host"}},
		"eventsCallback": map[string]any{"url": "https://api.example/v1/webhooks/operator-events", "token": "callback-token"},
	})
	getStatus, getBody := x.serve(x.s.LogsRequest(t, "/logs", time.Now()))

	if want := `{"lines":["app: listening on :8080"]}` + "\n"; status != http.StatusOK || string(body) != want {
		t.Fatalf("the POST answered %d with %s, want 200 and %s", status, body, want)
	}
	if want := `{"lines":["from the operator buffer"]}` + "\n"; getStatus != http.StatusOK || string(getBody) != want {
		t.Errorf("the signed GET answered %d with %s, want 200 and %s", getStatus, getBody, want)
	}
	if len(l.calls) != 1 || l.calls[0].SubscriptionID != "sub-1" || l.calls[0].Bindings["docker-host"]["subscriptionId"] != "sub-host" {
		t.Errorf("the logs function got %+v, want one call with sub-1's docker-host binding", l.calls)
	}
	if l.platform != "https://api.example" || l.token != "callback-token" {
		t.Errorf("the logs function reaches the platform at %q with token %q, want the request's events callback", l.platform, l.token)
	}
}

func TestServeLogs_RefusesARequestItMustNotServeBeforeTheLogsFunction(t *testing.T) {
	stale := time.Now().Add(-webhook.ReplaySkew - time.Minute).UTC().Format(time.RFC3339Nano)
	cases := []struct {
		name    string
		request func(x *harness) *http.Request
		status  int
	}{
		{"an unsigned POST is a 401", func(x *harness) *http.Request {
			r := x.s.HookRequest(t, "/logs", x.logsBody(nil))
			r.Header.Del("X-Mitstack-Signature-Ed25519")
			return r
		}, http.StatusUnauthorized},
		{"a stale createdAt is a 401", func(x *harness) *http.Request {
			return x.s.HookRequest(t, "/logs", x.logsBody(map[string]any{"createdAt": stale}))
		}, http.StatusUnauthorized},
		{"a token minted for another subscription is a 401", func(x *harness) *http.Request {
			other := mintOBO(t, x.s, webhook.OBOClaims{Audience: "plain", OrgID: "org-1", AppID: "app-1", SubscriptionID: "sub-2"})
			return x.s.HookRequest(t, "/logs", x.logsBody(map[string]any{"obo": other}))
		}, http.StatusUnauthorized},
		{"a lifecycle verb sent to /logs is a 400", func(x *harness) *http.Request {
			return x.s.HookRequest(t, "/logs", x.logsBody(map[string]any{"operation": "subscribe"}))
		}, http.StatusBadRequest},
		{"a logs request sent to /hook is a 400", func(x *harness) *http.Request {
			return x.s.HookRequest(t, "/hook", x.logsBody(nil))
		}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &logsReader{lines: []string{"must not leave the block"}}
			f := &kittest.FakeResource{}
			x := newHarness(t, f, servesLogs(t, l))

			status, body := x.serve(tc.request(x))

			if status != tc.status || handler.RefusedClass(decodeResponse(t, body).Error) == "" {
				t.Errorf("status = %d, want a %d refusal with a class; body: %s", status, tc.status, body)
			}
			if len(l.calls) != 0 {
				t.Errorf("the logs function ran %d times, want none", len(l.calls))
			}
			assertCalls(t, f, [3]int{})
		})
	}
}

func TestServeLogs_AReplayedRequestIsRefusedAndReadsNoLines(t *testing.T) {
	l := &logsReader{lines: []string{"one line"}}
	x := newHarness(t, &kittest.FakeResource{}, servesLogs(t, l))
	body := x.logsBody(nil)

	first, _ := x.serve(x.s.HookRequest(t, "/logs", body))
	replay, _ := x.serve(x.s.HookRequest(t, "/logs", body))

	if first != http.StatusOK || replay != http.StatusUnauthorized || len(l.calls) != 1 {
		t.Errorf("first and replayed status = %d, %d with %d reads; want 200, 401 and one read", first, replay, len(l.calls))
	}
}

func TestServeLogs_AnswersWhatTheLogsFunctionReturnsWithTheRequestsSecretsScrubbed(t *testing.T) {
	const secret = "hcloud-token-s3cret"
	type answer = func(context.Context, contract.Desired) ([]string, error)
	cases := map[string]struct {
		read          answer
		status        int
		refusingClass string
	}{
		"lines echoing a secret are a 200": {func(context.Context, contract.Desired) ([]string, error) { return []string{"with " + secret}, nil }, http.StatusOK, ""},
		"a refusal is a 400 with its class": {func(context.Context, contract.Desired) ([]string, error) {
			return nil, handler.Refuse("no-container", "none yet")
		}, http.StatusBadRequest, "no-container"},
		"an error is a 500": {func(context.Context, contract.Desired) ([]string, error) { return nil, errors.New("read " + secret) }, http.StatusInternalServerError, ""},
		"a panic is a 500":  {func(context.Context, contract.Desired) ([]string, error) { panic(secret) }, http.StatusInternalServerError, ""},
	}
	for name, tc := range cases {
		x := newHarness(t, &kittest.FakeResource{}, then(servesBlock(listsLogs(t)), func(c *handler.Config) { c.SubscriptionLogs = tc.read }))

		status, body := x.postLogs(map[string]any{"secrets": map[string]any{"hetzner": map[string]string{"token": secret}}})

		if status != tc.status || strings.Contains(string(body), secret) || (tc.refusingClass != "" && handler.RefusedClass(decodeResponse(t, body).StatusDetail) != tc.refusingClass) {
			t.Errorf("%s: answered %d with %s, want %d with the secret scrubbed and class %q", name, status, body, tc.status, tc.refusingClass)
		}
	}
}

func TestNew_RefusesALogsDeclarationWithoutItsFunction(t *testing.T) {
	naming := map[string]func(*handler.Config){
		"Config.SubscriptionLogs": servesBlock(listsLogs(t)),
		"does not list logs":      then(servesBlock(plainV1Beta(t)), func(c *handler.Config) { c.SubscriptionLogs = (&logsReader{}).read }),
	}
	for want, config := range naming {
		cfg := baseConfig(t, &kittest.FakeResource{}, kittest.NewSigner(t, "kid-1", 7))
		config(&cfg)

		if _, err := handler.New(cfg); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("handler.New error = %v, want a refusal naming %q", err, want)
		}
	}
}
