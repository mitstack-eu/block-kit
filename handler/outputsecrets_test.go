// outputsecrets_test.go pins the wire-safety half of the same change:
// before the handler writes any response, an Outputs value that is exactly
// one of the request's secrets.github.* values is refused — never scrubbed
// silently — naming the output key and never the value, through the same
// *handler.Refused / RefusedClass machinery errors.go already gives a
// block's own refusals. The class this refusal carries is
// "secret-in-output" (CONTRACT for code-author: use exactly this string, so
// RefusedClass(resp.StatusDetail) reads it back).
//
// The matching rule the ticket asks for is deliberately exact-value, not
// substring: an output value equal to a secret value is refused, but an
// output value that merely CONTAINS a secret value (the way weather-app's
// "repo" output embeds the login secrets.github.owner names) is left alone.
// scrubResponse (dispatch.go:305..309) already does substring redaction for
// the two free-text fields it owns (Error, StatusDetail); this is a
// different, additional gate over Outputs, and it must not reuse that
// substring behaviour or the owner case below would wrongly refuse.
//
// SecretOutputs is explicitly out of scope: it is where a Resource is
// supposed to put a secret it legitimately wants to return, so the gate
// must never inspect it.
package handler_test

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// wantRefusedClass is the class name this file's tests pin for the new
// output-secret gate. See the file doc for why this exact string is the
// CONTRACT, not an implementation detail either side can pick freely.
const wantRefusedClass = "secret-in-output"

// leakyHandoverFake is a minimal HandoverResource whose DeleteWithRequest
// returns a single configurable output, isolated from handoverFake's own
// snapshot/destroy semantics in handover_test.go so this file does not
// entangle with those tests.
type leakyHandoverFake struct {
	OutputKey   string
	OutputValue string
}

func (f *leakyHandoverFake) Ensure(ctx context.Context, d contract.Desired) error { return nil }

func (f *leakyHandoverFake) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	return contract.Observation{State: contract.Absent}, nil
}

func (f *leakyHandoverFake) Delete(ctx context.Context, id contract.Identity) error { return nil }

func (f *leakyHandoverFake) DeleteWithRequest(ctx context.Context, d contract.Desired) (map[string]string, error) {
	return map[string]string{f.OutputKey: f.OutputValue}, nil
}

var _ handler.Resource = (*leakyHandoverFake)(nil)
var _ handler.HandoverResource = (*leakyHandoverFake)(nil)

// captureLog redirects the standard logger into a buffer for the duration of
// the test, mirroring TestServe_NeverLeaksSecretsInResponseOrLogs in
// secrets_test.go.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// TestServe_ValidateOutputExactlyMatchingASecretValueIsRefused pins the
// first half of the fourth acceptance criterion: a Resource whose Observe
// returns secrets.github.token verbatim as an output value gets an error
// response naming the output key, and the token appears nowhere in the
// response body or the captured log.
func TestServe_ValidateOutputExactlyMatchingASecretValueIsRefused(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const tokenValue = "ghp_leaked1234567890abcdef"
	f := &kittest.FakeResource{Count: 1, Outputs: map[string]string{"leakedToken": tokenValue}}
	h := newHandler(t, f, s)
	logBuf := captureLog(t)

	status, raw := postHook(t, h, s, envelope("nonce-out-secret-validate", map[string]any{
		"operation": "validate",
		"secrets":   map[string]any{"github": map[string]any{"token": tokenValue}},
	}))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a refused response), body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if !strings.Contains(resp.Error, "leakedToken") {
		t.Errorf("Error = %q, want it to name the output key %q", resp.Error, "leakedToken")
	}
	if got := handler.RefusedClass(resp.StatusDetail); got != wantRefusedClass {
		t.Errorf("RefusedClass(StatusDetail) = %q, want %q (StatusDetail: %q)", got, wantRefusedClass, resp.StatusDetail)
	}

	kittest.AssertNoSecret(t, "response body", string(raw), tokenValue)
	kittest.AssertNoSecret(t, "log output", logBuf.String(), tokenValue)
}

// TestServe_UnsubscribeDeleteWithRequestOutputExactlyMatchingASecretValueIsRefused
// pins the second half of the fourth acceptance criterion: the same refusal
// fires when the leaking output comes from DeleteWithRequest rather than
// Observe.
func TestServe_UnsubscribeDeleteWithRequestOutputExactlyMatchingASecretValueIsRefused(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const tokenValue = "ghp_leakedondelete1234567890"
	f := &leakyHandoverFake{OutputKey: "leakedOnDelete", OutputValue: tokenValue}
	h := newHandler(t, f, s)
	logBuf := captureLog(t)

	status, raw := postHook(t, h, s, envelope("nonce-out-secret-delete", map[string]any{
		"operation": "unsubscribe",
		"secrets":   map[string]any{"github": map[string]any{"token": tokenValue}},
	}))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a refused response), body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if !strings.Contains(resp.Error, "leakedOnDelete") {
		t.Errorf("Error = %q, want it to name the output key %q", resp.Error, "leakedOnDelete")
	}
	if got := handler.RefusedClass(resp.StatusDetail); got != wantRefusedClass {
		t.Errorf("RefusedClass(StatusDetail) = %q, want %q (StatusDetail: %q)", got, wantRefusedClass, resp.StatusDetail)
	}

	kittest.AssertNoSecret(t, "response body", string(raw), tokenValue)
	kittest.AssertNoSecret(t, "log output", logBuf.String(), tokenValue)
}

// TestServe_ObserveOutputContainingASecretSubstringLikeOwnerLoginIsAllowed
// pins the fifth acceptance criterion: an output whose value merely CONTAINS
// a secrets-map value (the way weather-app's "repo" output embeds the login
// secrets.github.owner) is not the exact-value match the gate refuses on,
// so the response is 2xx with that output intact.
func TestServe_ObserveOutputContainingASecretSubstringLikeOwnerLoginIsAllowed(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const ownerValue = "acme-co"
	const tokenValue = "ghp_realtoken1234567890abcdef"
	const repoOutput = ownerValue + "/weather-app"
	f := &kittest.FakeResource{Count: 1, Outputs: map[string]string{"repo": repoOutput}}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope("nonce-out-owner-allowed", map[string]any{
		"operation": "validate",
		"secrets":   map[string]any{"github": map[string]any{"owner": ownerValue, "token": tokenValue}},
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a non-secret value embedded in an output must be allowed, body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Outputs["repo"] != repoOutput {
		t.Errorf(`Outputs["repo"] = %q, want %q intact`, resp.Outputs["repo"], repoOutput)
	}
}

// TestServe_SecretOutputsCarryingASecretValueIsNotRefused pins that the gate
// never inspects SecretOutputs: it is the field a Resource is meant to
// return a legitimate secret value through, so a value there identical to a
// request secret must reach the wire unrefused and unscrubbed.
//
// kittest.FakeResource has no SecretOutputs field, so this uses the
// requestObserverFake defined in observewithrequest_test.go — same package,
// same reasoning as leakyHandoverFake above for not extending the shared
// fixture.
func TestServe_SecretOutputsCarryingASecretValueIsNotRefused(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const tokenValue = "ghp_legitsecretoutput1234567890"
	f := &requestObserverFake{SecretOutputs: map[string]string{"apiToken": tokenValue}}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope("nonce-secretoutputs-ok", map[string]any{
		"operation": "validate",
		"secrets":   map[string]any{"github": map[string]any{"token": tokenValue}},
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — SecretOutputs is where a secret belongs, body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.SecretOutputs["apiToken"] != tokenValue {
		t.Errorf(`SecretOutputs["apiToken"] = %q, want the block's own secret output, untouched`, resp.SecretOutputs["apiToken"])
	}
}

// TestServe_EmptyOutputValueIsNotRefusedEvenWithAnUnsetSecretPresent pins
// that the gate's exact-value match must not treat "" as a secret. A
// secrets group can legitimately carry an unset optional secret as an empty
// string, and a Resource can legitimately return an empty output value (a
// field that is simply blank right now) — neither is a leak, and the two
// must not combine into a false refusal the way a naive "output value
// equals some secret value" check would if it never excluded "".
func TestServe_EmptyOutputValueIsNotRefusedEvenWithAnUnsetSecretPresent(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	const tokenValue = "ghp_realtoken_for_empty_case_1234567890"
	f := &kittest.FakeResource{Count: 1, Outputs: map[string]string{"blankField": "", "status": "ok"}}
	h := newHandler(t, f, s)

	status, raw := postHook(t, h, s, envelope("nonce-out-empty-secret-allowed", map[string]any{
		"operation": "validate",
		"secrets":   map[string]any{"github": map[string]any{"unsetSecret": "", "token": tokenValue}},
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an empty output value must not be refused just because an unset secret is also empty, body: %s", status, raw)
	}
	assertConforms(t, raw)

	resp := decodeResponse(t, raw)
	if resp.Outputs["blankField"] != "" {
		t.Errorf(`Outputs["blankField"] = %q, want "" intact`, resp.Outputs["blankField"])
	}
	if resp.Outputs["status"] != "ok" {
		t.Errorf(`Outputs["status"] = %q, want %q intact`, resp.Outputs["status"], "ok")
	}
}
