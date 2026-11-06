package handler_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

var verifyOnlyManifest = func() string {
	rows, err := contract.ManifestVerdicts()
	if err != nil {
		panic(err)
	}
	for _, row := range rows {
		if row.Name == "capability-sendcloud.yaml" {
			return string(row.Manifest)
		}
	}
	panic("the verdict table has no capability-sendcloud.yaml row")
}()

const (
	entered    = "sk-entered-by-the-person-7c1"
	enteredPub = "pk-entered-by-the-person-2d9"
)

type fakeVerifier struct {
	calls  []handler.ConnectionCheck
	answer handler.Verification
	err    error
}

func (f *fakeVerifier) VerifyConnection(_ context.Context, c handler.ConnectionCheck) (handler.Verification, error) {
	f.calls = append(f.calls, c)
	return f.answer, f.err
}

func verifyHarness(t *testing.T, v *fakeVerifier) *harness {
	s := kittest.NewSigner(t, "kid-1", 7)
	h, err := handler.New(handler.Config{Manifest: []byte(verifyOnlyManifest), JWKSURL: s.JWKSServer(t), Verifier: v})
	if err != nil {
		t.Fatalf("handler.New: %v", err)
	}
	return &harness{t: t, s: s, h: h, audience: "sendcloud"}
}

func (x *harness) verify(fields map[string]any) (int, []byte) {
	x.t.Helper()
	all := map[string]any{"operation": "verify-connection", "orgId": "org-1", "capabilitySlug": "sendcloud",
		"enteredCredential": map[string]any{"public_key": enteredPub, "private_key": entered}}
	for k, v := range fields {
		all[k] = v
	}
	body := kittest.Envelope(fmt.Sprintf("nonce-%d", nonces.Add(1)), time.Now(), all)
	return x.serve(x.s.HookRequest(x.t, "/hook", body))
}

func TestVerifyConnection_AnswersEachVerdictWithoutAResourceOrAnOBOTokenAndHandsOverTheCheck(t *testing.T) {
	for _, verdict := range []string{contract.VerdictVerified, contract.VerdictRejected, contract.VerdictUnreachable} {
		t.Run(verdict, func(t *testing.T) {
			v := &fakeVerifier{answer: handler.Verification{Verdict: verdict, Detail: "HTTP 200"}}
			x := verifyHarness(t, v)

			status, raw := x.verify(nil)

			assertConforms(t, raw)
			if got := decodeResponse(t, raw); status != http.StatusOK || got.Verdict != verdict || got.StatusDetail != "HTTP 200" {
				t.Errorf("status, verdict, detail = %d, %q, %q; want 200, %q, HTTP 200", status, got.Verdict, got.StatusDetail, verdict)
			}
			if c := v.calls; len(c) != 1 || c[0].CapabilitySlug != "sendcloud" || c[0].OrgID != "org-1" || c[0].Credential["private_key"] != entered || c[0].Credential["public_key"] != enteredPub || len(c[0].Credential) != 2 {
				t.Errorf("the verifier was handed %+v, want the slug, org and both entered fields", v.calls)
			}
		})
	}
}

func TestVerifyConnection_ScrubsTheCredentialFromTheDetailAndFromAnError(t *testing.T) {
	logs := captureLog(t)
	for name, v := range map[string]*fakeVerifier{
		"a detail":  {answer: handler.Verification{Verdict: contract.VerdictRejected, Detail: "the vendor echoed " + entered}},
		"an error":  {err: errors.New("dialing failed for key " + entered)},
		"a refusal": {err: handler.Refuse("vendor-refused", "the vendor named %s", enteredPub)},
	} {
		t.Run(name, func(t *testing.T) {
			_, raw := verifyHarness(t, v).verify(nil)

			kittest.AssertNoSecret(t, "the answer", string(raw), entered, enteredPub)
			kittest.AssertNoSecret(t, "the log", logs.String(), entered, enteredPub)
		})
	}
}

func TestVerifyConnection_AnswersNoVerdictWhenTheVerifierFailsOrAnswersNoneOfThree(t *testing.T) {
	for name, v := range map[string]*fakeVerifier{
		"an error":        {err: errors.New("the vendor client broke")},
		"an unknown word": {answer: handler.Verification{Verdict: "maybe"}},
	} {
		t.Run(name, func(t *testing.T) {
			status, raw := verifyHarness(t, v).verify(nil)

			if resp := decodeResponse(t, raw); status < 500 || resp.Verdict != "" {
				t.Errorf("status, verdict = %d, %q; want a 5xx error and no verdict", status, resp.Verdict)
			}
		})
	}
}

func TestVerifyConnection_IsNeverReplayedFromTheIdempotencyCache(t *testing.T) {
	v := &fakeVerifier{answer: handler.Verification{Verdict: contract.VerdictVerified}}
	x := verifyHarness(t, v)
	for range 2 {
		x.verify(map[string]any{"idempotencyKey": "same"})
	}

	if len(v.calls) != 2 {
		t.Errorf("the verifier was asked %d times for two requests sharing a key, want 2: a credential may have been revoked since", len(v.calls))
	}
}

func TestVerifyConnection_RefusesAnUnsignedRequest(t *testing.T) {
	x := verifyHarness(t, &fakeVerifier{})
	req := x.s.HookRequest(t, "/hook", kittest.Envelope("nonce-unsigned", time.Now(), map[string]any{"operation": "verify-connection", "orgId": "org-1",
		"capabilitySlug": "sendcloud", "enteredCredential": map[string]any{"private_key": entered}}))
	req.Header.Set("X-Mitstack-Signature-Ed25519", "ed25519=AAAA")

	if status, _ := x.serve(req); status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a request whose signature does not verify", status)
	}
}

func TestVerifyConnection_RefusesWhatTheOperatorOrTheRequestDoesNotAllow(t *testing.T) {
	v := &fakeVerifier{answer: handler.Verification{Verdict: contract.VerdictVerified}}
	only, block := verifyHarness(t, v), newHarness(t, &kittest.FakeResource{})
	cases := []struct {
		name  string
		send  func() (int, []byte)
		class string
	}{
		{"another capability's slug", func() (int, []byte) { return only.verify(map[string]any{"capabilitySlug": "mollie"}) }, "wrong-capability"},
		{"a subscribe sent to a verify-only operator", func() (int, []byte) { return only.post(map[string]any{"operation": "subscribe"}) }, "undeclared-operation"},
		{"a verify-connection sent to a block", func() (int, []byte) { return block.verify(map[string]any{"capabilitySlug": "test-block"}) }, "no-connection-check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, raw := tc.send()

			if resp := decodeResponse(t, raw); status != http.StatusBadRequest || handler.RefusedClass(resp.StatusDetail) != tc.class {
				t.Errorf("answered %d %q, want 400 refused:%s", status, resp.StatusDetail, tc.class)
			}
		})
	}
	if len(v.calls) != 0 {
		t.Errorf("the verifier was asked %d times, want none", len(v.calls))
	}
}

func TestNew_HoldsTheVerifierToTheManifestsListOfOperations(t *testing.T) {
	jwks := kittest.NewSigner(t, "kid-1", 7).JWKSServer(t)
	cases := []struct {
		name string
		cfg  handler.Config
		want string
	}{
		{"the operation listed and no verifier", handler.Config{Manifest: []byte(verifyOnlyManifest), JWKSURL: jwks}, "Config.Verifier is nil"},
		{"a verifier and the operation not listed", handler.Config{Resource: &kittest.FakeResource{}, Manifest: []byte(strings.Replace(verifyOnlyManifest, "[verify-connection]", "[validate]", 1)), JWKSURL: jwks, Verifier: &fakeVerifier{}}, "does not list verify-connection"},
		{"no resource and another operation", handler.Config{Manifest: []byte(strings.Replace(verifyOnlyManifest, "[verify-connection]", "[validate]", 1)), JWKSURL: jwks}, "Config.Resource is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handler.New(tc.cfg)

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("handler.New error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
