// A request failing the trust boundary, a wrong path or a wrong method is answered before the Resource is consulted, a refusal always with a class.
package handler_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/verify/kittest"
	"github.com/mitstack-eu/block-kit/webhook"
)

func TestServe_AnswersBeforeTheResourceIsConsulted(t *testing.T) {
	bare := map[string]any{"operation": "subscribe", "subscriptionId": "sub-1", "blockId": "block-1", "orgId": "org-1"}
	jwksDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(jwksDown.Close)
	signed := func(fields map[string]any, at time.Time) func(x *harness) *http.Request {
		return func(x *harness) *http.Request {
			return x.s.HookRequest(t, "/hook", kittest.Envelope(fmt.Sprintf("nonce-%d", nonces.Add(1)), at, fields))
		}
	}
	withLogs := func(lines ...string) func(*handler.Config) {
		return func(c *handler.Config) {
			c.Logs = webhook.NewLogBuffer(10)
			for _, l := range lines {
				c.Logs.Append(l)
			}
		}
	}
	type untrusted struct {
		name    string
		config  []func(*handler.Config)
		request func(x *harness) *http.Request
		status  int
		check   func(t *testing.T, body []byte)
	}
	cases := []untrusted{
		{name: "a request with no key id is a 401", request: func(x *harness) *http.Request {
			r := x.request(nil)
			r.Header.Del("X-Mitstack-Key-Id")
			return r
		}, status: http.StatusUnauthorized},
		{name: "a forged signature is a 401", request: func(x *harness) *http.Request {
			body := x.envelope(nil)
			r := x.s.HookRequest(t, "/hook", body)
			r.Header.Set("X-Mitstack-Signature-Ed25519", kittest.NewSigner(t, "kid-1", 99).SignBody(body))
			return r
		}, status: http.StatusUnauthorized},
		{name: "a body tampered after signing is a 401", request: func(x *harness) *http.Request {
			original := x.envelope(map[string]any{"operation": "subscribe"})
			r := x.s.HookRequest(t, "/hook", original)
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(string(original), "subscribe", "unsubscrib", 1)))
			return r
		}, status: http.StatusUnauthorized},
		{name: "a JWKS that cannot be fetched is a 503", config: []func(*handler.Config){func(c *handler.Config) { c.JWKSURL = jwksDown.URL }},
			request: func(x *harness) *http.Request { return x.request(nil) }, status: http.StatusServiceUnavailable},
		{name: "a stale createdAt is a 401", request: signed(bare, time.Now().Add(-webhook.ReplaySkew-time.Minute)), status: http.StatusUnauthorized},
		{name: "an invalid OBO token is a 401", request: func(x *harness) *http.Request { return x.request(map[string]any{"obo": "not-a-real-obo-token"}) },
			status: http.StatusUnauthorized},
		{name: "an envelope missing the required blockId is a 400", request: signed(map[string]any{"operation": "subscribe", "subscriptionId": "sub-1", "orgId": "org-1"}, time.Now()),
			status: http.StatusBadRequest},
		{name: "an unknown path is a 404", request: func(*harness) *http.Request { return httptest.NewRequest(http.MethodGet, "/nope", nil) }, status: http.StatusNotFound},
		{name: "a GET on /hook is a 405", request: func(*harness) *http.Request { return httptest.NewRequest(http.MethodGet, "/hook", nil) }, status: http.StatusMethodNotAllowed},
		{name: "a POST on /logs is a 405", request: func(*harness) *http.Request { return httptest.NewRequest(http.MethodPost, "/logs", nil) }, status: http.StatusMethodNotAllowed},
		{name: "an unsigned GET /logs is a 401", config: []func(*handler.Config){withLogs()},
			request: func(*harness) *http.Request { return httptest.NewRequest(http.MethodGet, "/logs", nil) }, status: http.StatusUnauthorized},
		{name: "GET /logs is a 404 when Config.Logs is nil", request: func(x *harness) *http.Request { return x.s.LogsRequest(t, "/logs", time.Now()) }, status: http.StatusNotFound},
		{name: "a signed GET /logs answers the appended lines", config: []func(*handler.Config){withLogs("hello from the operator")},
			request: func(x *harness) *http.Request { return x.s.LogsRequest(t, "/logs", time.Now()) }, status: http.StatusOK,
			check: func(t *testing.T, body []byte) {
				var doc struct {
					Lines []string `json:"lines"`
				}
				if err := json.Unmarshal(body, &doc); err != nil || !slices.Contains(doc.Lines, "hello from the operator") {
					t.Errorf("lines = %v (decode error %v), want the appended log line; body: %s", doc.Lines, err, body)
				}
			}},
	}
	for _, op := range []string{"subscribe", "upgrade", "validate", "unsubscribe"} {
		cases = append(cases, untrusted{name: "a " + op + " with no obo field is a 401 with a fixed error",
			request: signed(map[string]any{"operation": op, "subscriptionId": "sub-1", "blockId": "block-1", "orgId": "org-1"}, time.Now()), status: http.StatusUnauthorized,
			check: func(t *testing.T, body []byte) {
				if got, want := decodeResponse(t, body).Error, "refused:untrusted-request: missing OBO token"; got != want {
					t.Errorf("error = %q, want the fixed text %q so it carries nothing from the request body", got, want)
				}
			}})
	}
	for name, claims := range map[string]webhook.OBOClaims{
		"another org":          {Audience: "plain", OrgID: "org-2", AppID: "app-1", SubscriptionID: "sub-1"},
		"another app":          {Audience: "plain", OrgID: "org-1", AppID: "app-2", SubscriptionID: "sub-1"},
		"another subscription": {Audience: "plain", OrgID: "org-1", AppID: "app-1", SubscriptionID: "sub-2"},
		"no block":             {OrgID: "org-1", AppID: "app-1", SubscriptionID: "sub-1"},
	} {
		cases = append(cases, untrusted{name: "a token naming " + name + " is a 401", config: []func(*handler.Config){servesBlock(plainV1Beta(t))},
			request: func(x *harness) *http.Request { return x.request(plainRequest(t, x.s, claims)) }, status: http.StatusUnauthorized})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &kittest.FakeResource{}
			x := newHarness(t, f, tc.config...)

			status, body := x.serve(tc.request(x))

			if status != tc.status {
				t.Errorf("status = %d, want %d, body: %s", status, tc.status, body)
			}
			if tc.check != nil {
				tc.check(t, body)
			}
			if status/100 == 4 {
				if resp := decodeResponse(t, body); handler.RefusedClass(resp.Error) == "" || resp.StatusDetail != resp.Error {
					t.Errorf("a %d is a refusal, so its error and statusDetail must both carry its class; body: %s", status, body)
				}
			}
			assertCalls(t, f, [3]int{})
		})
	}
}

func TestServe_ReplayedNonceIs401AndNeverReachesTheResource(t *testing.T) {
	f := &kittest.FakeResource{}
	x := newHarness(t, f)
	body := x.envelope(nil)

	first, _ := x.serve(x.s.HookRequest(t, "/hook", body))
	replay, _ := x.serve(x.s.HookRequest(t, "/hook", body))

	if first != http.StatusOK || replay != http.StatusUnauthorized {
		t.Errorf("first and replayed status = %d, %d; want 200, 401", first, replay)
	}
	if f.EnsureCalls != 1 {
		t.Errorf("EnsureCalls = %d, want 1: the replay never reaches the Resource", f.EnsureCalls)
	}
}

func servesBlock(manifest []byte) func(*handler.Config) {
	return func(c *handler.Config) {
		c.Manifest = manifest
		c.Observers = map[string]handler.Observer{"aws.ec2-container": &observer{name: "aws.ec2-container", cloud: newFakeCloud()}}
	}
}

func plainRequest(t *testing.T, s *kittest.Signer, claims webhook.OBOClaims) map[string]any {
	return map[string]any{"blockId": "plain", "orgId": "org-1", "appId": "app-1", "subscriptionId": "sub-1", "obo": mintOBO(t, s, claims)}
}

func TestServe_ARequestMintedForOneBlockIsRefusedOnAnotherBlocksRoute(t *testing.T) {
	x := newHarness(t, &kittest.FakeResource{}, servesBlock(plainV1Beta(t)))
	other := newHarness(t, &kittest.FakeResource{}, servesBlock(withManifestEdit(t, plainV1Beta(t), "  id: plain\n", "  id: other\n")))
	operator := http.NewServeMux()
	operator.Handle("/plain/", http.StripPrefix("/plain", x.h))
	operator.Handle("/other/", http.StripPrefix("/other", other.h))
	body := x.envelope(plainRequest(t, x.s, webhook.OBOClaims{Audience: "plain", OrgID: "org-1", AppID: "app-1", SubscriptionID: "sub-1"}))

	var got []int
	for _, route := range []string{"/plain/hook", "/other/hook"} {
		w := httptest.NewRecorder()
		operator.ServeHTTP(w, x.s.HookRequest(t, route, body))
		got = append(got, w.Code)
	}

	if !slices.Equal(got, []int{http.StatusOK, http.StatusUnauthorized}) {
		t.Errorf("one request signed for plain answered %v on the plain and other routes, want [200 401]", got)
	}
}
