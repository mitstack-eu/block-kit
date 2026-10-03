package contract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
)

// ManifestSchema is the JSON Schema for a block manifest under
// ManifestAPIVersion. It is what GenerateManifestSchema builds from
// BlockManifest's types, regenerated with
// go run ./cmd/manifest-schema > contract/manifest.schema.json.
//
//go:embed manifest.schema.json
var ManifestSchema []byte

// manifestSchemaID is the $id of ManifestSchema.
const manifestSchemaID = "https://mitstack.dev/schemas/block/manifest.schema.json"

// inputValueRef points at the one definition of InputValue, which nests.
const inputValueRef = "#/$defs/inputValue"

// GenerateManifestSchema builds the JSON Schema for a block manifest under
// ManifestAPIVersion by reflection over BlockManifest's types and json
// tags. Every object is closed, a field whose tag has no omitempty is
// required, and a required string must not be empty, as ParseManifest
// holds a manifest. A requirement's in is one string or a list of strings,
// and a resource input is a scalar or an object of inputs. The output ends
// in a line break and is the same on every call.
func GenerateManifestSchema() ([]byte, error) {
	doc, err := typeSchema(blockManifestType, false)
	if err != nil {
		return nil, err
	}
	props := doc["properties"].(map[string]any)
	props["apiVersion"].(map[string]any)["const"] = ManifestAPIVersion
	props["type"].(map[string]any)["const"] = "block"
	doc["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	doc["$id"] = manifestSchemaID
	doc["title"] = "mitstack block manifest " + ManifestAPIVersion
	doc["description"] = "A block manifest under " + ManifestAPIVersion + ": its params, its requirements by kind, " +
		"the resources it creates in creation order, the resources it provides, its lifecycle and its observability. " +
		"Generated from the operator kit's contract.BlockManifest types; ParseManifest also refuses a resource name used twice " +
		"and a provides entry that names no resource."
	doc["$defs"] = map[string]any{
		"inputValue": map[string]any{
			"description": "A resource input: a binding or a literal, or an object of named inputs.",
			"anyOf": []any{
				map[string]any{"type": []any{"string", "number", "boolean"}},
				map[string]any{"type": "object", "additionalProperties": map[string]any{"$ref": inputValueRef}},
			},
		},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("contract: encoding the manifest schema: %w", err)
	}
	return buf.Bytes(), nil
}

// typeSchema returns the schema for a value of Go type t. required marks a
// string that must not be empty.
func typeSchema(t reflect.Type, required bool) (map[string]any, error) {
	if t == inputValueType {
		return map[string]any{"$ref": inputValueRef}, nil
	}
	switch t.Kind() {
	case reflect.String:
		s := map[string]any{"type": "string"}
		if required {
			s["minLength"] = 1
		}
		return s, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Pointer:
		return typeSchema(t.Elem(), required)
	case reflect.Slice:
		items, err := typeSchema(t.Elem(), false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		values, err := typeSchema(t.Elem(), false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": values}, nil
	case reflect.Struct:
		return structSchema(t)
	}
	return nil, fmt.Errorf("contract: the manifest schema has no form for the Go type %s", t)
}

// structSchema returns the closed object schema for the struct t.
func structSchema(t reflect.Type) (map[string]any, error) {
	props := map[string]any{}
	required := []any{}
	for _, f := range manifestFields(t) {
		var s map[string]any
		var err error
		if f.oneOrList {
			s = map[string]any{"oneOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}}
		} else if s, err = typeSchema(t.Field(f.index).Type, f.required); err != nil {
			return nil, err
		}
		props[f.name] = s
		if f.required {
			required = append(required, f.name)
		}
	}
	s := map[string]any{"type": "object", "additionalProperties": false, "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s, nil
}
