package handler_test

// A capability operator answers verify-connection with a verdict, through a Verifier, and with no Resource when it is the manifest's only operation.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

const verifyOnlyManifest = `apiVersion: mitstack/v1beta
type: capability
metadata:
  id: sendcloud
  name: Sendcloud
  owner: mitstack
spec:
  version: 1.0.0
  description: Parcels an app ships through Sendcloud.
  connection:
    mechanism: stored-token
    tokenSchema:
      - { name: public_key, secret: true }
      - { name: private_key, secret: true }
  lifecycle:
    endpoint: https://api.mitstack.dev/operators/capabilities/sendcloud/hook
    operations: [verify-connection]
    contract: kit/v1
`

const (
	entered    = "sk-entered-by-the-person-7c1"
	enteredPub = "pk-entered-by-the-person-2d9"
)

type fakeVerifier struct {
	mu     sync.Mutex
	calls  []handler.ConnectionCheck
	answer handler.Verification
	err    error
}

func (f *fakeVerifier) VerifyConnection(_ context.Context, c handler.ConnectionCheck) (handler.Verification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	return f.answer, f.err
}

func (f *fakeVerifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func verifyHarness(t *testing.T, v *fakeVerifier) *harness {
	t.Helper()
	s := kittest.NewSigner(t, "kid-1", 7)
	h, err := handler.New(handler.Config{Manifest: []byte(verifyOnlyManifest), JWKSURL: s.JWKSServer(t), Verifier: v})
	if err != nil {
		t.Fatalf("handler.New: %v", err)
	}
	return &harness{t: t, s: s, h: h, audience: "sendcloud"}
}

func (x *harness) verify(fields map[string]any) (int, []byte) {
	x.t.Helper()
	all := map[string]any{
		"operation":         "verify-connection",
		"orgId":             "org-1",
		"capabilitySlug":    "sendcloud",
		"enteredCredential": map[string]any{"public_key": enteredPub, "private_key": entered},
	}
	for k, v := range fields {
		all[k] = v
	}
	body := kittest.Envelope(fmt.Sprintf("nonce-%d", nonces.Add(1)), time.Now(), all)
	return x.serve(x.s.HookRequest(x.t, "/hook", body))
}

func TestVerifyConnection_AnswersTheVerifiersVerdictWithoutAResourceOrAnOBOToken(t *testing.T) {
	v := &fakeVerifier{answer: handler.Verification{Verdict: contract.VerdictRejected, Detail: "the vendor answered 401"}}
	x := verifyHarness(t, v)

	status, raw := x.verify(nil)

	assertConforms(t, raw)
	resp := decodeResponse(t, raw)
	if status != http.StatusOK || resp.Verdict != contract.VerdictRejected || resp.StatusDetail != "the vendor answered 401" {
		t.Errorf("status, verdict, detail = %d, %q, %q; want 200, rejected, the vendor's 401", status, resp.Verdict, resp.StatusDetail)
	}
	if v.count() != 1 {
		t.Fatalf("the verifier was asked %d times, want once", v.count())
	}
	want := handler.ConnectionCheck{CapabilitySlug: "sendcloud", OrgID: "org-1", Credential: map[string]string{"public_key": enteredPub, "private_key": entered}}
	got := v.calls[0]
	if got.CapabilitySlug != want.CapabilitySlug || got.OrgID != want.OrgID || got.Credential["private_key"] != entered || got.Credential["public_key"] != enteredPub || len(got.Credential) != 2 {
		t.Errorf("the verifier was handed %+v, want %+v", got, want)
	}
}

func TestVerifyConnection_AnswersEachOfTheThreeVerdicts(t *testing.T) {
	for _, verdict := range []string{contract.VerdictVerified, contract.VerdictRejected, contract.VerdictUnreachable} {
		t.Run(verdict, func(t *testing.T) {
			x := verifyHarness(t, &fakeVerifier{answer: handler.Verification{Verdict: verdict, Detail: "HTTP 200"}})

			status, raw := x.verify(nil)

			if got := decodeResponse(t, raw); status != http.StatusOK || got.Verdict != verdict {
				t.Errorf("status, verdict = %d, %q; want 200, %q", status, got.Verdict, verdict)
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
			x := verifyHarness(t, v)

			_, raw := x.verify(nil)

			kittest.AssertNoSecret(t, "the answer", string(raw), entered, enteredPub)
			kittest.AssertNoSecret(t, "the log", logs.String(), entered, enteredPub)
		})
	}
}

func TestVerifyConnection_AnErrorFromTheVerifierIsNotAVerdict(t *testing.T) {
	x := verifyHarness(t, &fakeVerifier{err: errors.New("the vendor client broke")})

	status, raw := x.verify(nil)

	if resp := decodeResponse(t, raw); status < 500 || resp.Verdict != "" || resp.Error == "" {
		t.Errorf("status, verdict, error = %d, %q, %q; want a 5xx error and no verdict", status, resp.Verdict, resp.Error)
	}
}

func TestVerifyConnection_RefusesAVerdictThatIsNoneOfTheThree(t *testing.T) {
	x := verifyHarness(t, &fakeVerifier{answer: handler.Verification{Verdict: "maybe"}})

	status, raw := x.verify(nil)

	if resp := decodeResponse(t, raw); status < 500 || resp.Verdict != "" {
		t.Errorf("status, verdict = %d, %q; want a 5xx error and no verdict", status, resp.Verdict)
	}
}

func TestVerifyConnection_RefusesAnotherCapabilitysSlugWithoutAskingTheVerifier(t *testing.T) {
	v := &fakeVerifier{answer: handler.Verification{Verdict: contract.VerdictVerified}}
	x := verifyHarness(t, v)

	status, raw := x.verify(map[string]any{"capabilitySlug": "mollie"})

	resp := decodeResponse(t, raw)
	if status != http.StatusBadRequest || handler.RefusedClass(resp.StatusDetail) != "wrong-capability" || v.count() != 0 {
		t.Errorf("status, detail, asked = %d, %q, %d; want 400, refused:wrong-capability and no ask", status, resp.StatusDetail, v.count())
	}
}

func TestVerifyConnection_IsNeverReplayedFromTheIdempotencyCache(t *testing.T) {
	v := &fakeVerifier{answer: handler.Verification{Verdict: contract.VerdictVerified}}
	x := verifyHarness(t, v)

	x.verify(map[string]any{"idempotencyKey": "same"})
	x.verify(map[string]any{"idempotencyKey": "same"})

	if v.count() != 2 {
		t.Errorf("the verifier was asked %d times for two requests sharing a key, want 2: a credential may have been revoked since", v.count())
	}
}

func TestVerifyConnection_RefusesAnUnsignedRequest(t *testing.T) {
	x := verifyHarness(t, &fakeVerifier{})
	body := kittest.Envelope("nonce-unsigned", time.Now(), map[string]any{"operation": "verify-connection", "orgId": "org-1",
		"capabilitySlug": "sendcloud", "enteredCredential": map[string]any{"private_key": entered}})
	req := x.s.HookRequest(t, "/hook", body)
	req.Header.Set("X-Mitstack-Signature-Ed25519", "ed25519=AAAA")

	status, _ := x.serve(req)

	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a request whose signature does not verify", status)
	}
}

func TestVerifyConnection_RefusesARequestMissingTheCredential(t *testing.T) {
	v := &fakeVerifier{answer: handler.Verification{Verdict: contract.VerdictVerified}}
	x := verifyHarness(t, v)

	status, _ := x.verify(map[string]any{"enteredCredential": map[string]any{}})

	if status != http.StatusBadRequest || v.count() != 0 {
		t.Errorf("status, asked = %d, %d; want 400 and no ask", status, v.count())
	}
}

func TestVerifyConnection_AVerifyOnlyOperatorRefusesEveryOtherVerb(t *testing.T) {
	x := verifyHarness(t, &fakeVerifier{})

	status, raw := x.post(map[string]any{"operation": "subscribe"})

	if resp := decodeResponse(t, raw); status != http.StatusBadRequest || handler.RefusedClass(resp.StatusDetail) != "undeclared-operation" {
		t.Errorf("a subscribe answered %d %q, want 400 refused:undeclared-operation", status, resp.StatusDetail)
	}
}

func TestVerifyConnection_ABlockOperatorRefusesIt(t *testing.T) {
	x := newHarness(t, &kittest.FakeResource{})

	status, raw := x.verify(map[string]any{"capabilitySlug": "test-block"})

	if resp := decodeResponse(t, raw); status != http.StatusBadRequest || handler.RefusedClass(resp.StatusDetail) != "no-connection-check" {
		t.Errorf("a verify-connection on a block answered %d %q, want 400 refused:no-connection-check", status, resp.StatusDetail)
	}
}

func TestNew_HoldsTheVerifierToTheManifestsListOfOperations(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	jwks := s.JWKSServer(t)
	cases := []struct {
		name string
		cfg  handler.Config
		want string
	}{
		{"the operation listed and no verifier", handler.Config{Manifest: []byte(verifyOnlyManifest), JWKSURL: jwks}, "Config.Verifier is nil"},
		{"a verifier and the operation not listed", handler.Config{Resource: &kittest.FakeResource{}, Manifest: []byte(oneResourceManifest), JWKSURL: jwks,
			Observers: map[string]handler.Observer{"r": observedThrough{&kittest.FakeResource{}}}, Verifier: &fakeVerifier{}}, "does not list verify-connection"},
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
