package contract_test

// A verify-connection carries the entered credential, the capability's slug and the connecting org, and no subscription.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

func verifyConnectionBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	fields := map[string]any{
		"operation":         "verify-connection",
		"orgId":             "org_acme",
		"capabilitySlug":    "sendcloud",
		"enteredCredential": map[string]any{"public_key": "pk-entered", "private_key": "sk-entered"},
	}
	if mutate != nil {
		mutate(fields)
	}
	return kittest.Envelope("nonce-verify", time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC), fields)
}

func TestDecodeRequest_ReadsAVerifyConnectionWithNoSubscriptionOrBlock(t *testing.T) {
	req, err := contract.DecodeRequest(verifyConnectionBody(t, nil))

	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.Operation != contract.OperationVerifyConnection || req.OrgID != "org_acme" || req.CapabilitySlug != "sendcloud" {
		t.Errorf("operation, orgId, capabilitySlug = %q, %q, %q; want verify-connection, org_acme, sendcloud", req.Operation, req.OrgID, req.CapabilitySlug)
	}
	if got := req.EnteredCredential["private_key"]; got != "sk-entered" || len(req.EnteredCredential) != 2 {
		t.Errorf("enteredCredential = %v, want the two entered fields", req.EnteredCredential)
	}
}

func TestDecodeRequest_RefusesAVerifyConnectionMissingWhatItNeeds(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		field  string
	}{
		{"no org", func(f map[string]any) { delete(f, "orgId") }, "orgId"},
		{"no capability slug", func(f map[string]any) { delete(f, "capabilitySlug") }, "capabilitySlug"},
		{"no credential", func(f map[string]any) { delete(f, "enteredCredential") }, "enteredCredential"},
		{"an empty credential", func(f map[string]any) { f["enteredCredential"] = map[string]any{} }, "enteredCredential"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := contract.DecodeRequest(verifyConnectionBody(t, tc.mutate))

			if err == nil || !strings.Contains(err.Error(), `"`+tc.field+`"`) {
				t.Errorf("DecodeRequest error = %v, want a refusal naming %s", err, tc.field)
			}
		})
	}
}

func TestDecodeRequest_AStateChangingVerbStillNeedsItsSubscription(t *testing.T) {
	body := kittest.Envelope("nonce-subscribe", time.Now(), map[string]any{"operation": "subscribe", "orgId": "org_acme"})

	_, err := contract.DecodeRequest(body)

	if err == nil || !strings.Contains(err.Error(), `"subscriptionId"`) {
		t.Errorf("DecodeRequest of a subscribe with no subscription = %v, want a refusal naming subscriptionId", err)
	}
}

func TestEnvelopeSchema_ListsVerifyConnectionAndItsTwoFields(t *testing.T) {
	doc := jsonObject(t, contract.EnvelopeSchema)
	props, _ := doc["properties"].(map[string]any)
	operation, _ := props["operation"].(map[string]any)

	enum, _ := operation["enum"].([]any)
	if !containsAny(enum, "verify-connection") {
		t.Errorf("envelope.schema.json operation enum = %v, want it to list \"verify-connection\"", enum)
	}
	for _, field := range []string{"capabilitySlug", "enteredCredential"} {
		if _, ok := props[field]; !ok {
			t.Errorf("envelope.schema.json properties = %q, want %s among them", sortedKeys(props), field)
		}
		if containsAny(anySlice(doc["required"]), field) {
			t.Errorf("envelope.schema.json requires %q for every operation, but only a verify-connection carries it", field)
		}
	}
	for _, field := range []string{"subscriptionId", "blockId"} {
		if containsAny(anySlice(doc["required"]), field) {
			t.Errorf("envelope.schema.json requires %q for every operation, but a verify-connection carries none", field)
		}
	}
}

func TestResponseSchema_DeclaresTheVerdictAsOneOfThree(t *testing.T) {
	doc := jsonObject(t, contract.ResponseSchema)
	verdict := schemaAt(t, doc, doc, "properties", "verdict")

	enum, _ := verdict["enum"].([]any)

	for _, want := range []string{"verified", "rejected", "unreachable"} {
		if !containsAny(enum, want) {
			t.Errorf("response.schema.json verdict enum = %v, want it to list %q", enum, want)
		}
	}
	if len(enum) != 3 {
		t.Errorf("response.schema.json verdict enum = %v, want exactly three verdicts", enum)
	}
}

func TestResponse_EncodesAVerdictUnderItsWireName(t *testing.T) {
	raw, err := json.Marshal(contract.Response{Verdict: contract.VerdictRejected, StatusDetail: "the vendor refused the key"})
	if err != nil {
		t.Fatal(err)
	}

	got := decodeResponse(t, raw)

	if got.Verdict != "rejected" || got.StatusDetail != "the vendor refused the key" {
		t.Errorf("response re-read from %s = %+v, want verdict rejected with its detail", raw, got)
	}
	if !strings.Contains(string(raw), `"verdict":"rejected"`) {
		t.Errorf("response encodes as %s, want the key verdict", raw)
	}
	if problems := kittest.CheckResponse(raw); len(problems) != 0 {
		t.Errorf("a verdict response does not conform to the response contract: %v", problems)
	}
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func containsAny(list []any, want string) bool {
	for _, e := range list {
		if e == any(want) {
			return true
		}
	}
	return false
}
