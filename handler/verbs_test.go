// The handler answers a block's errors and its outputs with exact bytes, and carries no request secret into an answer, a log or a later request.
package handler_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/verify/kittest"
	"github.com/mitstack-eu/block-kit/webhook"
)

// guardSecret is a request secret every row sends, so the scrub and the output refusal are part of what the bytes pin.
const guardSecret = "guard-secret-token-value"

func guardSecrets() map[string]any {
	return map[string]any{"github": map[string]any{"token": guardSecret}}
}

func stateOf(s contract.State) *contract.State { return &s }

func TestServe_AnswersAWrappedRefusalAndTheOutputGateWithExactBytes(t *testing.T) {
	cases := []struct {
		name, op, body string
		r              handler.Resource
		fields         map[string]any
		status         int
	}{
		{name: "a wrapped refusal is still a 400 whose statusDetail carries its class", op: "subscribe", status: http.StatusBadRequest,
			r:    &kittest.FakeResource{EnsureErr: fmt.Errorf("ensure: %w", handler.Refuse("quota-exceeded", "org quota exhausted"))},
			body: `{"outputs":null,"error":"ensure: refused:quota-exceeded: org quota exhausted","statusDetail":"refused:quota-exceeded: org quota exhausted"}`},
		{name: "the gate passes an output merely containing a request value, an empty output beside an unset secret, and a secret output", op: "validate", status: http.StatusOK,
			r:      &resource{FakeResource: &kittest.FakeResource{Count: 1, Outputs: map[string]string{"repo": "acme-co/weather-app", "blankField": ""}}, secretOutputs: map[string]string{"apiToken": guardSecret}},
			fields: map[string]any{"secrets": map[string]any{"github": map[string]any{"owner": "acme-co", "token": guardSecret, "unsetSecret": ""}}},
			body: `{"outputs":{"blankField":"","repo":"acme-co/weather-app"},"secretOutputs":{"apiToken":"` + guardSecret + `"},"absent":false,` +
				`"resources":{"r":{"observe":{"state":"present","outputs":{"blankField":"","repo":"acme-co/weather-app"},"type":"aws.vpc"}}},"resourceSecretOutputs":{"r":{"apiToken":"` + guardSecret + `"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureLog(t)

			status, raw := newHarness(t, tc.r).post(map[string]any{"operation": tc.op, "secrets": guardSecrets()}, tc.fields)

			if status != tc.status || string(raw) != tc.body {
				t.Errorf("%s answered %d %s\nwant %d %s", tc.op, status, raw, tc.status, tc.body)
			}
			assertConforms(t, raw)
			kittest.AssertNoSecret(t, "log output", logged.String(), guardSecret)
		})
	}
}

// A Handler that stashed one verb's secrets in its own state would hand them to a later, unrelated request here.
func TestServe_OneVerbsSecretsNeverReachALaterRequest(t *testing.T) {
	r := fake(&kittest.FakeResource{Count: 1})
	x := newHarness(t, r)

	first, _ := x.post(map[string]any{"operation": "validate", "subscriptionId": "sub-scope-a", "secrets": guardSecrets()})
	second, _ := x.post(map[string]any{"operation": "validate", "subscriptionId": "sub-scope-b"})

	if first != http.StatusOK || second != http.StatusOK || len(r.observed) != 2 {
		t.Fatalf("statuses %d, %d with %d observes, want 200, 200 and one observe each", first, second, len(r.observed))
	}
	if got := r.observed[0].Secrets["github"]["token"]; got != guardSecret {
		t.Errorf("the first request's Observe saw token %q, want its own secret", got)
	}
	if got := r.observed[1].Secrets; len(got) != 0 {
		t.Errorf("the second request's Observe saw Secrets %v, want none", got)
	}
}

func TestRefusedClass(t *testing.T) {
	cases := map[string]string{
		"refused:dependents-remain: two apps still bind this database":               "dependents-remain",
		handler.Refuse(handler.ClassHostCannotRun, "Docker does not answer").Error(): "host-cannot-run",
		handler.Refuse("", "a refusal with no class").Error():                        "unclassified",
		"":                   "",
		"some other detail":  "",
		"refusedwithnocolon": "",
		"REFUSED:upper-case": "",
	}
	for detail, want := range cases {
		if got := handler.RefusedClass(detail); got != want {
			t.Errorf("RefusedClass(%q) = %q, want %q", detail, got, want)
		}
	}
}

// The block's own error quotes the secret, so scrubbing must cover block-authored text in the answer and the log alike.
func TestServe_NeverLeaksAnySecretTheRequestCarries(t *testing.T) {
	const secret, input, appSecret, eventsToken = "top-secret-db-password-value", "top-secret-input-value-here", "top-secret-app-secret-value", "top-secret-events-callback-token"
	logged := captureLog(t)
	x := newHarness(t, &kittest.FakeResource{EnsureErr: fmt.Errorf("upstream rejected credential %s", secret)})
	obo := mintOBO(t, x.s, webhook.OBOClaims{Audience: x.audience, OrgID: "org-1", AppID: "app-1", SubscriptionID: "sub-1"})

	status, raw := x.post(map[string]any{
		"appId":          "app-1",
		"obo":            obo,
		"secrets":        map[string]any{"db": map[string]any{"password": secret}},
		"secretInputs":   map[string]any{"apiKey": input},
		"appSecrets":     map[string]any{"masterKey": appSecret},
		"eventsCallback": map[string]any{"url": "http://127.0.0.1:1/events", "token": eventsToken},
	})

	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for the Ensure error, body: %s", status, raw)
	}
	for what, haystack := range map[string]string{"response body": string(raw), "log output": logged.String()} {
		kittest.AssertNoSecret(t, what, haystack, secret, input, appSecret, obo, eventsToken)
	}
}
