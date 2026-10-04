package contract_test

// resource_identity_request_test.go pins the request half of handing a named
// resource's later verbs what its ensure stored: a request naming one
// resource may carry, beside that resource's recorded inputs, the identity
// its ensure stored under "resourceIdentity", an object of strings, and
// Desired carries it across to the resource's handler. An identity with no
// resource to bind it to is refused, naming both fields, and the envelope
// schema declares the field optional and dependent on "resource". A request
// naming no resource decodes to no identity, so every request golden keeps
// the outcome request_goldens_pin_test.go pins for it.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// namedResourceIdentity is the identity the named resource's ensure stored.
var namedResourceIdentity = map[string]string{"groupId": "sg-0abc", "vpcId": "vpc-0abc"}

func TestDecodeRequest_ReadsTheNamedResourcesInputsAndStoredIdentityBackOnDesired(t *testing.T) {
	raw := mutateSubscribeGolden(t, func(m map[string]any) {
		m["operation"] = "unsubscribe"
		m["resource"] = "firewall"
		m["resourceInputs"] = namedResourceInputs
		m["resourceIdentity"] = namedResourceIdentity
	})

	req, err := contract.DecodeRequest(raw)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if !reflect.DeepEqual(req.ResourceIdentity, namedResourceIdentity) {
		t.Errorf("ResourceIdentity = %v, want %v", req.ResourceIdentity, namedResourceIdentity)
	}

	d := req.Desired()
	if d.Resource != "firewall" {
		t.Errorf("Desired().Resource = %q, want %q", d.Resource, "firewall")
	}
	wantRawInputs(t, "Desired().ResourceInputs", d.ResourceInputs)
	if !reflect.DeepEqual(d.ResourceIdentity, namedResourceIdentity) {
		t.Errorf("Desired().ResourceIdentity = %v, want %v", d.ResourceIdentity, namedResourceIdentity)
	}
	if d.SubscriptionID != req.SubscriptionID || !reflect.DeepEqual(d.Params, req.Params) {
		t.Errorf("Desired() lost the subscription or params: %+v", d)
	}
}

func TestDecodeRequest_RefusesAResourceIdentityWithNoResourceNamingBothFields(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"no resource":    func(m map[string]any) { m["resourceIdentity"] = namedResourceIdentity },
		"empty resource": func(m map[string]any) { m["resource"] = ""; m["resourceIdentity"] = namedResourceIdentity },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := contract.DecodeRequest(mutateSubscribeGolden(t, mutate))
			if err == nil {
				t.Fatal("DecodeRequest accepted a resource identity with no resource to bind it to, want it refused")
			}
			for _, field := range []string{`"resourceIdentity"`, `"resource"`} {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("DecodeRequest error = %q, want it to name the field %s", err.Error(), field)
				}
			}
		})
	}
}

func TestDecodeRequest_EveryValidGoldenNamesNoResourceAndDecodesToNoIdentity(t *testing.T) {
	for _, name := range []string{"subscribe", "upgrade", "unsubscribe", "validate"} {
		t.Run(name, func(t *testing.T) {
			req := decodeGolden(t, name)
			if req.ResourceIdentity != nil {
				t.Errorf("%s names no resource, but decodes to ResourceIdentity %v", name, req.ResourceIdentity)
			}
			if d := req.Desired(); d.ResourceIdentity != nil {
				t.Errorf("%s names no resource, but Desired() carries ResourceIdentity %v", name, d.ResourceIdentity)
			}
		})
	}
}

func TestRequestMarshal_EncodesTheResourceIdentityUnderItsWireName(t *testing.T) {
	raw, err := json.Marshal(contract.Request{Resource: "firewall", ResourceIdentity: map[string]string{"groupId": "sg-0abc"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"resourceIdentity":{"groupId":"sg-0abc"}`) {
		t.Errorf("json.Marshal = %s, want it to carry %s", raw, `"resourceIdentity":{"groupId":"sg-0abc"}`)
	}
}

func TestEnvelopeSchema_DeclaresResourceIdentityAsAnOptionalObjectOfStringsThatNeedsAResource(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(contract.EnvelopeSchema, &doc); err != nil {
		t.Fatalf("contract.EnvelopeSchema is not valid JSON: %v", err)
	}
	if req, ok := doc["required"].([]any); ok {
		for _, r := range req {
			if r == "resourceIdentity" {
				t.Error(`envelope.schema.json requires "resourceIdentity", but a request naming no resource must stay valid`)
			}
		}
	}

	props := schemaObject(t, doc, "", "properties")
	identity, ok := props["resourceIdentity"].(map[string]any)
	if !ok {
		t.Fatal(`envelope.schema.json declares no "resourceIdentity" property`)
	}
	if !hasType(identity, "object") {
		t.Errorf("properties.resourceIdentity type = %v, want object", schemaTypes(identity))
	}
	values := schemaObject(t, identity, "properties.resourceIdentity", "additionalProperties")
	if !hasType(values, "string") {
		t.Errorf("properties.resourceIdentity values type = %v, want string", schemaTypes(values))
	}

	dependent := schemaObject(t, doc, "", "dependentRequired")
	needs, ok := dependent["resourceIdentity"].([]any)
	if !ok || !reflect.DeepEqual(needs, []any{"resource"}) {
		t.Errorf(`dependentRequired.resourceIdentity = %v, want ["resource"]`, dependent["resourceIdentity"])
	}
}
