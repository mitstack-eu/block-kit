// The handler answers a block's errors and its outputs with exact bytes, and carries no request secret into an answer, a log or a later request.
package handler_test

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/verify/kittest"
	"github.com/mitstack-eu/block-kit/webhook"
)

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

func TestServe_ARefusalKeepsTheClassItsBlockChoseAndMasksEachSecretInTheTextAroundIt(t *testing.T) {
	const detail = "the host runs amd64 and the image is arm64"
	appSecretHost := map[string]any{"appSecrets": map[string]any{"DB_HOST": "host"}}
	for _, tc := range []struct {
		name, wantError, wantDetail, wantClass, wantLog string
		err                                             error
		secrets                                         map[string]any
	}{
		{name: "an app secret spelling a word of the class is masked in the detail alone", err: handler.Refuse(handler.ClassHostCannotRun, detail),
			secrets: appSecretHost, wantDetail: "refused:host-cannot-run: the [REDACTED] runs amd64 and the image is arm64"},
		{name: "a wrapped refusal masks the secret in its wrapping too", err: fmt.Errorf("ensure on the host: %w", handler.Refuse(handler.ClassHostCannotRun, detail)),
			secrets: appSecretHost, wantDetail: "refused:host-cannot-run: the [REDACTED] runs amd64 and the image is arm64",
			wantError: "ensure on the [REDACTED]: refused:host-cannot-run: the [REDACTED] runs amd64 and the image is arm64"},
		{name: "a secret equal to a word of the detail is masked wherever it stands there", err: handler.Refuse(handler.ClassHostCannotRun, "cannot pull: the host cannot read what it cannot"),
			secrets: map[string]any{"secretInputs": map[string]any{"registryUser": "cannot"}}, wantDetail: "refused:host-cannot-run: [REDACTED] pull: the host [REDACTED] read what it [REDACTED]"},
		{name: "an app secret that starts another shows no part of either in the answer, the event or the log line of a refusal with no class", err: handler.Refuse("", "no route to quartz.lagoon.example.test"),
			secrets: map[string]any{"appSecrets": map[string]any{"TENANT": "quartz", "TENANT_HOST": "quartz.lagoon"}}, wantClass: "unclassified", wantDetail: "refused:unclassified: no route to [REDACTED].example.test", wantLog: " handler: block returned a refusal with no class: no route to [REDACTED].example.test\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, logged := newFakeEventsServer(t), captureLog(t)
			wantError, wantClass := cmp.Or(tc.wantError, tc.wantDetail), cmp.Or(tc.wantClass, handler.ClassHostCannotRun)

			status, raw := newHarness(t, &kittest.FakeResource{EnsureErr: tc.err}).post(tc.secrets, map[string]any{"eventsCallback": map[string]any{"url": events.URL, "token": "events-tok"}})

			resp := decodeResponse(t, raw)
			if class := handler.RefusedClass(resp.StatusDetail); status != http.StatusBadRequest || class != wantClass || resp.StatusDetail != tc.wantDetail || resp.Error != wantError {
				t.Errorf("answered %d, class %q, statusDetail %q, error %q\nwant 400, class %q, statusDetail %q, error %q", status, class, resp.StatusDetail, resp.Error, wantClass, tc.wantDetail, wantError)
			}
			if got := events.recorded(); len(got) != 1 || got[0].Detail != wantError {
				t.Errorf("events = %+v, want one failed event whose detail reads %q", got, wantError)
			}
			if got := logged.String(); !strings.HasSuffix(got, tc.wantLog) || strings.Count(got, "\n") != strings.Count(tc.wantLog, "\n") {
				t.Errorf("log = %q, want it to hold only a line ending %q", got, tc.wantLog)
			}
		})
	}
}

func TestServe_ARefusalIsMaskedWholeWhenASecretReadsAcrossTheEdgeOfItsClass(t *testing.T) {
	refusal := handler.Refuse(handler.ClassHostCannotRun, "the image is arm64")
	for _, tc := range []struct {
		name, secret, inside, wantError, wantDetail string
		err                                         error
	}{
		{name: "across the class's end and the detail", secret: "run: the", err: refusal,
			wantDetail: "refused:host-cannot-[REDACTED] image is arm64"},
		{name: "across the prefix and the class", secret: "sed:host", err: refusal,
			wantDetail: "refu[REDACTED]-cannot-run: the image is arm64"},
		{name: "across the wrapping and the refusal, in the error alone", secret: "e: refu", err: fmt.Errorf("ensure: %w", refusal),
			wantDetail: "refused:host-cannot-run: the image is arm64", wantError: "ensur[REDACTED]sed:host-cannot-run: the image is arm64"},
		{name: "across the detail and the wrapping after it, in the error alone", secret: "arm64 (att", err: fmt.Errorf("%w (attempt 2)", refusal),
			wantDetail: "refused:host-cannot-run: the image is arm64", wantError: "refused:host-cannot-run: the image is [REDACTED]empt 2)"},
		{name: "across the detail and the wrapping after it, where another secret ends the detail and overlaps it", secret: "64 (att", inside: "arm64", err: fmt.Errorf("%w (attempt 2)", refusal),
			wantDetail: "refused:host-cannot-run: the image is [REDACTED]", wantError: "refused:host-cannot-run: the image is [REDACTED]empt 2)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureLog(t)
			events := newFakeEventsServer(t)
			wantError := cmp.Or(tc.wantError, tc.wantDetail)

			status, raw := newHarness(t, &kittest.FakeResource{EnsureErr: tc.err}).post(map[string]any{"appSecrets": map[string]any{"EDGE": tc.secret, "INSIDE": tc.inside}}, map[string]any{"eventsCallback": map[string]any{"url": events.URL, "token": "events-tok"}})

			resp := decodeResponse(t, raw)
			if status != http.StatusBadRequest || resp.StatusDetail != tc.wantDetail || resp.Error != wantError {
				t.Errorf("answered %d, statusDetail %q, error %q\nwant 400, statusDetail %q, error %q", status, resp.StatusDetail, resp.Error, tc.wantDetail, wantError)
			}
			got := events.recorded()
			if len(got) != 1 || got[0].Detail != wantError {
				t.Errorf("events = %+v, want one failed event whose detail reads %q", got, wantError)
			}
			for what, haystack := range map[string]string{"response body": string(raw), "event": fmt.Sprint(got), "log output": logged.String()} {
				kittest.AssertNoSecret(t, what, haystack, tc.secret, tc.inside)
			}
			if !strings.Contains(logged.String(), "a secret reads across the edge of refusal class host-cannot-run") {
				t.Errorf("log = %q, want a line naming the class whose refusal was masked whole", logged.String())
			}
		})
	}
}

func TestNeedsAppName_AnswersNeedsAppOnlyForARequestNamingNeitherTheAppNorItsName(t *testing.T) {
	cases := map[string]struct {
		d    contract.Desired
		want string
	}{
		"neither":    {contract.Desired{Params: map[string]string{handler.ParamAppName: ""}}, handler.ClassNeedsApp},
		"nil params": {contract.Desired{}, handler.ClassNeedsApp},
		"its name":   {contract.Desired{Params: map[string]string{handler.ParamAppName: "shop"}}, ""},
		"the app":    {contract.Desired{Identity: contract.Identity{AppID: "app-1"}}, ""},
	}
	for name, c := range cases {
		err := handler.NeedsAppName(c.d)
		if got := handler.RefusedClass(fmt.Sprint(err)); got != c.want || (c.want == "") != (err == nil) {
			t.Errorf("%s: NeedsAppName = %v, want class %q", name, err, c.want)
		}
	}
}

// The block's own error quotes the secrets, so scrubbing must cover block-authored text in the answer and the log alike, a block's own secret included, since it comes back on every later request.
func TestServe_NeverLeaksAnySecretTheRequestCarries(t *testing.T) {
	const secret, input, appSecret, eventsToken, own = "top-secret-db-password-value", "top-secret-input-value-here", "top-secret-app-secret-value", "top-secret-events-callback-token", "top-secret-own-gateway-ca-key"
	logged := captureLog(t)
	x := newHarness(t, &kittest.FakeResource{EnsureErr: fmt.Errorf("upstream rejected credential %s signed with %s", secret, own)})
	obo := mintOBO(t, x.s, webhook.OBOClaims{Audience: x.audience, OrgID: "org-1", AppID: "app-1", SubscriptionID: "sub-1"})

	for _, op := range []string{"subscribe", "upgrade"} {
		status, raw := x.post(map[string]any{
			"operation": op, "appId": "app-1", "obo": obo,
			"secrets":        map[string]any{"db": map[string]any{"password": secret}},
			"secretInputs":   map[string]any{"apiKey": input},
			"appSecrets":     map[string]any{"masterKey": appSecret},
			"ownSecrets":     map[string]any{"gatewayCaKey": own},
			"eventsCallback": map[string]any{"url": "http://127.0.0.1:1/events", "token": eventsToken},
		})

		if status != http.StatusInternalServerError {
			t.Fatalf("%s: status = %d, want 500 for the Ensure error, body: %s", op, status, raw)
		}
		for what, haystack := range map[string]string{"response body": string(raw), "log output": logged.String()} {
			kittest.AssertNoSecret(t, op+" "+what, haystack, secret, input, appSecret, own, obo, eventsToken)
		}
	}
}

func TestRequireBound_AnswersTheUnboundRequirementRefusalNamingTheRequirementOnlyWhenTheRequestBindsNothingForIt(t *testing.T) {
	bound := map[string]map[string]string{"cluster": {"endpoint": "https://10.0.0.1"}}
	boundAll := map[string][]map[string]string{"cluster": {{"endpoint": "https://10.0.0.1"}}}
	cases := map[string]struct {
		d    contract.Desired
		want string
	}{
		"nothing":     {contract.Desired{}, handler.ClassUnboundRequirement},
		"another one": {contract.Desired{Bindings: map[string]map[string]string{"volume": {"id": "v1"}}}, handler.ClassUnboundRequirement},
		"one binding": {contract.Desired{Bindings: bound}, ""},
		"every offer": {contract.Desired{BindingsAll: boundAll}, ""},
	}
	for name, c := range cases {
		err := handler.RequireBound(c.d, "cluster")
		if got := handler.RefusedClass(fmt.Sprint(err)); got != c.want || (c.want == "") != (err == nil) {
			t.Errorf("%s: RequireBound = %v, want class %q", name, err, c.want)
		}
		if err != nil && !strings.Contains(err.Error(), `"cluster"`) {
			t.Errorf("%s: %v, want it to name the requirement", name, err)
		}
	}
}
