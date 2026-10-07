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

// typeIDRef and typeIDsRef point at the definitions of one type id and of
// an entry's type, which every requires and resources entry shares.
const (
	typeIDRef  = "#/$defs/typeId"
	typeIDsRef = "#/$defs/typeIds"
)

// GenerateManifestSchema builds the JSON Schema for a block manifest under
// ManifestAPIVersion by reflection over BlockManifest's types and json
// tags. Every object is closed, a field whose tag has no omitempty is
// required, and a required string or list must not be empty, as
// ParseManifest holds a manifest. A requires or resources entry is a type
// id or a closed object whose type, one id or a list, is required.
// The output ends in a line break and is the same on every call.
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
	doc["description"] = "A block manifest under " + ManifestAPIVersion + ": its params, its requirements by exact type, " +
		"the resources it creates by exact type, the resources it provides, its lifecycle and its observability. " +
		"Generated from the operator kit's contract.BlockManifest types; ParseManifest also refuses a resource name used twice, " +
		"a requirement reference used twice, an entry listing several types without a name, " +
		"and a provides entry that names no resource."
	doc["$defs"] = map[string]any{
		"typeId": map[string]any{
			"description": "A resource type id, <capability>.<name> in lowercase kebab case.",
			"type":        "string",
			"pattern":     typeIDPattern.String(),
		},
		"typeIds": map[string]any{
			"description": "One type id, or a list of the type ids the entry may be met or created by.",
			"oneOf": []any{
				map[string]any{"$ref": typeIDRef},
				map[string]any{"type": "array", "items": map[string]any{"$ref": typeIDRef}, "minItems": 1, "uniqueItems": true},
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
// string or a list that must not be empty.
func typeSchema(t reflect.Type, required bool) (map[string]any, error) {
	switch t {
	case requirementType:
		return entrySchema("A requirement: a type id, or an object of its type, its name and many.", true), nil
	case resourceType:
		return entrySchema("A resource the block creates: a type id, or an object of its type and its name.", false), nil
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
		s := map[string]any{"type": "array", "items": items}
		if required {
			s["minItems"] = 1
		}
		return s, nil
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
	for _, f := range manifestFields(t, v1betaDecoder.tag) {
		s, err := typeSchema(t.Field(f.index).Type, f.required)
		if err != nil {
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

// entrySchema returns the schema of a requires or resources entry: a type
// id, or a closed object whose type is required, with many for a
// requirement.
func entrySchema(description string, requirement bool) map[string]any {
	props := map[string]any{
		"type": map[string]any{"$ref": typeIDsRef},
		"name": map[string]any{"type": "string", "minLength": 1},
	}
	if requirement {
		props["many"] = map[string]any{"type": "boolean"}
	}
	return map[string]any{
		"description": description,
		"oneOf": []any{
			map[string]any{"$ref": typeIDRef},
			map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": []any{"type"}},
		},
	}
}
