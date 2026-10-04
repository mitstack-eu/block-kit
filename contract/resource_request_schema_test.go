package contract_test

// resource_request_schema_test.go pins that the two schemas declare the
// fields that run a verb on one declared resource: the envelope declares
// "resource" and "resourceInputs" as optional, and the response declares
// the identity an ensure stored as an object of strings on the ensure
// outcome.

import (
	"encoding/json"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

func TestEnvelopeSchema_DeclaresResourceAndResourceInputsAsOptional(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(contract.EnvelopeSchema, &doc); err != nil {
		t.Fatalf("contract.EnvelopeSchema is not valid JSON: %v", err)
	}
	if req, ok := doc["required"].([]any); ok {
		for _, r := range req {
			if r == "resource" || r == "resourceInputs" {
				t.Errorf("envelope.schema.json requires %q, but a request naming no resource must stay valid", r)
			}
		}
	}

	props := schemaObject(t, doc, "", "properties")
	resource, ok := props["resource"].(map[string]any)
	if !ok {
		t.Fatal(`envelope.schema.json declares no "resource" property`)
	}
	if !hasType(resource, "string") {
		t.Errorf("properties.resource type = %v, want string", schemaTypes(resource))
	}

	inputs, ok := props["resourceInputs"].(map[string]any)
	if !ok {
		t.Fatal(`envelope.schema.json declares no "resourceInputs" property`)
	}
	if !hasType(inputs, "object") {
		t.Errorf("properties.resourceInputs type = %v, want object", schemaTypes(inputs))
	}
	if extra, ok := inputs["additionalProperties"].(map[string]any); ok {
		if len(schemaTypes(extra)) == 1 && hasType(extra, "string") {
			t.Error("properties.resourceInputs only admits string values, but an input may be a mapping too")
		}
	}
}

func TestResponseSchema_DeclaresIdentityOnTheEnsureOutcomeAsAnObjectOfStrings(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(contract.ResponseSchema, &doc); err != nil {
		t.Fatalf("contract.ResponseSchema is not valid JSON: %v", err)
	}
	props := schemaObject(t, doc, "", "properties")
	resources := schemaObject(t, props, "properties", "resources")
	outcome := schemaObject(t, resources, "properties.resources", "additionalProperties")
	calls := schemaObject(t, outcome, "properties.resources.additionalProperties", "properties")
	ensure := schemaObject(t, calls, "properties.resources.additionalProperties.properties", "ensure")
	fields := schemaObject(t, ensure, "the ensure outcome", "properties")

	identity, ok := fields["identity"].(map[string]any)
	if !ok {
		t.Fatal(`the ensure outcome in response.schema.json declares no "identity" field`)
	}
	if !hasType(identity, "object") {
		t.Errorf("ensure identity type = %v, want object", schemaTypes(identity))
	}
	values := schemaObject(t, identity, "the ensure identity", "additionalProperties")
	if !hasType(values, "string") {
		t.Errorf("ensure identity values type = %v, want string", schemaTypes(values))
	}
}
