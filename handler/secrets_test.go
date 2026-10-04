package handler_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
	"github.com/mitstack-eu/operator-kit/webhook"
)

// TestServe_NeverLeaksSecretsInResponseOrLogs pins the CONTRACT's secrets
// section: a request carrying secrets, secretInputs, appSecrets, an OBO
// token and an events callback token must produce a response body AND log
// lines containing none of them — even when the block's own error message
// quotes one, since safe.LogSafe/events.ScrubDetail must run on every
// string this package writes, not just the ones it authored itself.
//
// ASSUMPTION: the handler logs via the standard library "log" package, the
// convention every other kit package documents (see webhook/logs.go's
// LogBuffer.Writer doc). This test redirects log.SetOutput itself, exactly
// as an operator's main() wires LogBuffer, since Config carries no logger
// field of its own.
func TestServe_NeverLeaksSecretsInResponseOrLogs(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)

	const secretValue = "top-secret-db-password-value"
	const secretInputValue = "top-secret-input-value-here"
	const appSecretValue = "top-secret-app-secret-value"
	const eventsToken = "top-secret-events-callback-token"

	oboToken, err := webhook.MintOBO(s.Private, s.Kid, webhook.OBOClaims{
		OrgID: "org-1", AppID: "app-1", SubscriptionID: "sub-1",
	}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("minting a test OBO token: %v", err)
	}

	// The block's own error message quotes the secret — proving scrubbing
	// runs on block-authored text too, not only on request-derived text.
	f := &kittest.FakeResource{EnsureErr: fmt.Errorf("upstream rejected credential %s", secretValue)}

	var logBuf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	h := newHandler(t, f, s)

	body := envelope(t, s, "nonce-secrets", map[string]any{
		"obo":          oboToken,
		"secrets":      map[string]any{"db": map[string]any{"password": secretValue}},
		"secretInputs": map[string]any{"apiKey": secretInputValue},
		"appSecrets":   map[string]any{"masterKey": appSecretValue},
		"eventsCallback": map[string]any{
			"url":   "http://127.0.0.1:1/events", // deliberately unreachable; advisory only
			"token": eventsToken,
		},
	})

	status, raw := postHook(t, h, s, body)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (the Ensure error), body: %s", status, raw)
	}

	kittest.AssertNoSecret(t, "response body", string(raw),
		secretValue, secretInputValue, appSecretValue, oboToken, eventsToken)
	kittest.AssertNoSecret(t, "log output", logBuf.String(),
		secretValue, secretInputValue, appSecretValue, oboToken, eventsToken)
}

// secretInputsManifest declares one resource whose inputs bind secret params,
// directly and inside a mapping, and one whose catalogued type marks an input
// secret. The scaffold resource's type, github.scaffold-contribution, marks
// none of its inputs secret, so only the params' secret: true makes config
// and env.DEPLOY_TOKEN secret there. The app resource's type, host.container,
// marks env secret, so every string under env is secret whatever it binds.
// image, files, port and env.DEPLOY_REGION are bound to non-secret params or
// literals.
const secretInputsManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: secret-inputs
  name: Secret Inputs
spec:
  version: 1.0.0
  params:
    - name: image
      label: Container image
    - name: templatePath
      label: Template path
    - name: region
      label: Deploy region
    - name: licenceKey
      label: Licence key
      secret: true
    - name: deployToken
      label: Deploy credential
      secret: true
  requires:
    - kind: host
    - kind: postgres-database
  resources:
    - name: scaffold
      type: github.scaffold-contribution
      inputs:
        files: params.templatePath
        config: params.licenceKey
        env:
          DEPLOY_TOKEN: params.deployToken
          DEPLOY_REGION: params.region
    - name: app
      type: host.container
      on: requires.host
      inputs:
        image: params.image
        port: 8080
        env:
          DATABASE_PASSWORD: requires.postgres-database.outputs.password
          DATABASE:
            USER: requires.postgres-database.outputs.username
  lifecycle:
    endpoint: https://secret-inputs.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

// The values the engine resolves each input to before it sends a request.
// Each secret is distinctive and long enough for safe.Scrub to redact, and
// none follows a credential-like key with ":" or "=", so events.ScrubDetail
// can never redact it by accident and hide a leak.
const (
	scaffoldTemplatePath = "templates/web-starter-q4115"
	scaffoldRegion       = "eu-central-q4115-frankfurt"
	scaffoldLicenceKey   = "licence-q4115-Qx7vT2mLp9RkWz3n"
	scaffoldDeployToken  = "deploy-q4115-Hn8sYc2VbJ5tQe6u"

	appImage      = "registry.example/tracker-q4115:4.2.1"
	appDBPassword = "dbpass-q4115-Lr3Wk9ZpF6xNa2Cg"
	appDBUser     = "dbuser-q4115-Tm4Jq8RvD1sKy7Pb"
)

// secretResourceInputs is the resourceInputs each declared resource of
// secretInputsManifest is sent on a request naming it.
var secretResourceInputs = map[string]map[string]any{
	"scaffold": {
		"files":  scaffoldTemplatePath,
		"config": scaffoldLicenceKey,
		"env": map[string]any{
			"DEPLOY_TOKEN":  scaffoldDeployToken,
			"DEPLOY_REGION": scaffoldRegion,
		},
	},
	"app": {
		"image": appImage,
		"port":  8080,
		"env": map[string]any{
			"DATABASE_PASSWORD": appDBPassword,
			"DATABASE":          map[string]any{"USER": appDBUser},
		},
	},
}

// redactionMarker is what safe.Scrub writes in place of a secret.
const redactionMarker = "[REDACTED]"

// secretInputsUnsubscribe is what one unsubscribe naming a resource of
// secretInputsManifest produced: the answer, every event the handler emitted
// and every log line written while it ran.
type secretInputsUnsubscribe struct {
	status int
	raw    []byte
	resp   contract.Response
	events []recordedEvent
	log    string
}

// serveSecretInputsUnsubscribe sends an unsubscribe naming resource, with its
// resourceInputs and NO secretInputs, to a handler over secretInputsManifest
// whose named resource is fake, capturing the events callback and the log.
func serveSecretInputsUnsubscribe(t *testing.T, resource string, fake *kittest.FakeResource) secretInputsUnsubscribe {
	t.Helper()
	calls := &callLog{}
	resources := map[string]handler.DeclaredResource{
		"scaffold": &recordedResource{name: "scaffold", log: calls, FakeResource: &kittest.FakeResource{}},
		"app":      &recordedResource{name: "app", log: calls, FakeResource: &kittest.FakeResource{}},
	}
	resources[resource] = &recordedResource{name: resource, log: calls, FakeResource: fake}

	var logBuf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	s := kittest.NewSigner(t, "kid-secret-inputs", 23)
	h := newHandler(t, nil, s, func(c *handler.Config) {
		c.Manifest = []byte(secretInputsManifest)
		c.Resources = resources
	})
	eventsSrv := newFakeEventsServer(t)

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-secret-inputs-"+resource, map[string]any{
		"operation":      "unsubscribe",
		"resource":       resource,
		"resourceInputs": secretResourceInputs[resource],
		"eventsCallback": eventsCallbackField(eventsSrv.URL, "events-tok-secret-inputs"),
	}))
	assertConforms(t, raw)
	return secretInputsUnsubscribe{
		status: status,
		raw:    raw,
		resp:   decodeResponse(t, raw),
		events: eventsSrv.Events(),
		log:    logBuf.String(),
	}
}

// resourceInputLeakPath is one way a value a resource echoes reaches what the
// handler writes on an unsubscribe: carrier returns the answer field that
// must hold the value, scrubbed.
type resourceInputLeakPath struct {
	name       string
	fake       *kittest.FakeResource
	wantStatus int
	carrier    func(t *testing.T, resp contract.Response) (field, text string)
}

// resourceInputLeakPaths returns the three ways an unsubscribe can echo
// value: a Delete that fails and quotes it, which reaches the error, the
// failed event and the log; a Delete refused quoting it, which reaches the
// error and the statusDetail; and a resource still standing after its Delete
// whose Observe statusDetail quotes it, which reaches the resource's observe
// entry on the absent:false answer.
func resourceInputLeakPaths(resource, value string) []resourceInputLeakPath {
	return []resourceInputLeakPath{
		{
			name:       "a failing delete quotes it",
			fake:       &kittest.FakeResource{Count: 1, DeleteErr: fmt.Errorf("removing the resource failed, upstream echoed %s", value)},
			wantStatus: http.StatusInternalServerError,
			carrier: func(t *testing.T, resp contract.Response) (string, string) {
				return "error", resp.Error
			},
		},
		{
			name:       "a refused delete quotes it",
			fake:       &kittest.FakeResource{Count: 1, DeleteErr: handler.Refuse("upstream-refused", "upstream would not remove the resource holding %s", value)},
			wantStatus: http.StatusBadRequest,
			carrier: func(t *testing.T, resp contract.Response) (string, string) {
				return "statusDetail", resp.StatusDetail
			},
		},
		{
			name:       "a resource left standing reports it in its status detail",
			fake:       &kittest.FakeResource{Count: 1, LeakOnDelete: true, StatusDetail: "still serving with " + value},
			wantStatus: http.StatusOK,
			carrier: func(t *testing.T, resp contract.Response) (string, string) {
				t.Helper()
				o := outcomeOf(t, resp, resource)
				if o.Observe == nil {
					t.Fatalf("resource %q has no observe outcome; resources: %+v", resource, resp.Resources)
				}
				return "resources." + resource + ".observe.statusDetail", o.Observe.StatusDetail
			},
		},
	}
}

// assertResourceInputNeverLeaks runs every leak path for one secret resource
// input of resource and fails if the value reaches the answer, any event the
// handler emits or the log, or if the answer field that carried it does not
// hold the redaction marker in its place.
func assertResourceInputNeverLeaks(t *testing.T, resource, secret string) {
	t.Helper()
	for _, path := range resourceInputLeakPaths(resource, secret) {
		t.Run(path.name, func(t *testing.T) {
			got := serveSecretInputsUnsubscribe(t, resource, path.fake)
			if got.status != path.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", got.status, path.wantStatus, got.raw)
			}
			field, text := path.carrier(t, got.resp)
			if !strings.Contains(text, redactionMarker) {
				t.Errorf("%s = %q, want the echoed value replaced by %s", field, text, redactionMarker)
			}
			if len(got.events) == 0 {
				t.Errorf("the handler emitted no event to the callback; it emits one per unsubscribe")
			}

			kittest.AssertNoSecret(t, "response body", string(got.raw), secret)
			for i, ev := range got.events {
				evRaw, err := json.Marshal(ev)
				if err != nil {
					t.Fatalf("encoding recorded event %d: %v", i, err)
				}
				kittest.AssertNoSecret(t, fmt.Sprintf("event %d", i), string(evRaw), secret)
			}
			kittest.AssertNoSecret(t, "log output", got.log, secret)
		})
	}
}

// TestServe_UnsubscribeScrubsAResourceInputBoundToASecretParam pins that an
// unsubscribe carrying no secretInputs still scrubs a resource input the
// manifest binds to a param marked secret: true, though the resource's
// catalogued type marks that input not secret.
func TestServe_UnsubscribeScrubsAResourceInputBoundToASecretParam(t *testing.T) {
	assertResourceInputNeverLeaks(t, "scaffold", scaffoldLicenceKey)
}

// TestServe_UnsubscribeScrubsASecretParamNestedInAMappingInput pins that a
// secret param bound inside a mapping input is scrubbed at the same nested
// path of the request's resource input, though the type marks the mapping
// input not secret.
func TestServe_UnsubscribeScrubsASecretParamNestedInAMappingInput(t *testing.T) {
	assertResourceInputNeverLeaks(t, "scaffold", scaffoldDeployToken)
}

// TestServe_UnsubscribeScrubsAnInputTheCataloguedTypeMarksSecret pins that
// every string under an input the resource's catalogued type marks secret
// (host.container's env) is scrubbed, at its top level and nested deeper,
// whatever the manifest binds it to.
func TestServe_UnsubscribeScrubsAnInputTheCataloguedTypeMarksSecret(t *testing.T) {
	t.Run("a string at the top of env", func(t *testing.T) {
		assertResourceInputNeverLeaks(t, "app", appDBPassword)
	})
	t.Run("a string nested inside env", func(t *testing.T) {
		assertResourceInputNeverLeaks(t, "app", appDBUser)
	})
}

// TestServe_ScrubbingSecretResourceInputsLeavesLiteralInputsAsTheyAre pins
// that only secret resource inputs are scrubbed: an error quoting a secret
// input alongside inputs bound to a literal or to a non-secret param, at the
// top level or nested in a mapping, keeps every non-secret value as it is.
func TestServe_ScrubbingSecretResourceInputsLeavesLiteralInputsAsTheyAre(t *testing.T) {
	cases := []struct {
		resource string
		secret   string
		literals []string
	}{
		{
			resource: "app",
			secret:   appDBPassword,
			literals: []string{appImage, "port 8080"},
		},
		{
			resource: "scaffold",
			secret:   scaffoldLicenceKey,
			literals: []string{scaffoldTemplatePath, scaffoldRegion},
		},
	}
	for _, c := range cases {
		t.Run(c.resource, func(t *testing.T) {
			message := fmt.Sprintf("upstream echoed %s for %s", c.secret, strings.Join(c.literals, " and "))
			got := serveSecretInputsUnsubscribe(t, c.resource,
				&kittest.FakeResource{Count: 1, DeleteErr: errors.New(message)})
			if got.status != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (the Delete error); body %s", got.status, got.raw)
			}

			kittest.AssertNoSecret(t, "response error", got.resp.Error, c.secret)
			for _, literal := range c.literals {
				if !strings.Contains(got.resp.Error, literal) {
					t.Errorf("response error = %q, want the non-secret input %q left as it is", got.resp.Error, literal)
				}
			}
			if n := strings.Count(got.resp.Error, redactionMarker); n != 1 {
				t.Errorf("response error = %q carries %s %d times, want once: only the secret input is scrubbed", got.resp.Error, redactionMarker, n)
			}
		})
	}
}
