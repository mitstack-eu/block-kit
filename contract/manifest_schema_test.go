package contract_test

// manifest_schema_test.go pins the JSON Schema the kit ships for a
// mitstack/v1beta manifest: the committed manifest.schema.json is exactly
// what contract.GenerateManifestSchema builds from the manifest types, so
// the schema and the types cannot drift apart, and it describes requirements
// and resources with exactly the keys the types name.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

func TestManifestSchema_IsWhatTheTypesGenerate(t *testing.T) {
	if len(bytes.TrimSpace(contract.ManifestSchema)) == 0 {
		t.Fatal("contract.ManifestSchema is empty, so comparing it with the types proves nothing")
	}
	generated, err := contract.GenerateManifestSchema()
	if err != nil {
		t.Fatalf("contract.GenerateManifestSchema(): %v", err)
	}
	if !bytes.Equal(generated, contract.ManifestSchema) {
		t.Errorf("contract/manifest.schema.json differs from what the manifest types generate; "+
			"run go run ./cmd/manifest-schema > contract/manifest.schema.json\ngenerated:\n%s\ncommitted:\n%s",
			generated, contract.ManifestSchema)
	}
}

// schemaDoc decodes contract.ManifestSchema.
func schemaDoc(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(contract.ManifestSchema, &doc); err != nil {
		t.Fatalf("contract.ManifestSchema is not a JSON object: %v", err)
	}
	return doc
}

// schemaAt follows path from node through the schema: each element names a
// key, and a node carrying a local "$ref" is replaced by what it points to
// in root before the next key is read.
func schemaAt(t *testing.T, root map[string]any, node map[string]any, path ...string) map[string]any {
	t.Helper()
	for i := 0; ; i++ {
		for hops := 0; ; hops++ {
			ref, ok := node["$ref"].(string)
			if !ok {
				break
			}
			if hops > 8 || !strings.HasPrefix(ref, "#/") {
				t.Fatalf("schema $ref %q is not a local reference the test can follow", ref)
			}
			target := any(root)
			for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				m, _ := target.(map[string]any)
				target = m[part]
			}
			next, ok := target.(map[string]any)
			if !ok {
				t.Fatalf("schema $ref %q points at nothing", ref)
			}
			node = next
		}
		if i == len(path) {
			return node
		}
		next, ok := node[path[i]].(map[string]any)
		if !ok {
			t.Fatalf("schema has no object at %s", strings.Join(path[:i+1], "."))
		}
		node = next
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func TestManifestSchema_DeclaresDraft2020_12AndAnID(t *testing.T) {
	doc := schemaDoc(t)
	if got, want := doc["$schema"], "https://json-schema.org/draft/2020-12/schema"; got != want {
		t.Errorf("ManifestSchema $schema = %v, want %q", got, want)
	}
	if id, _ := doc["$id"].(string); id == "" {
		t.Errorf("ManifestSchema $id = %v, want a non-empty string", doc["$id"])
	}
}

// A requirement and a resource are closed objects with exactly the keys
// ADR 0083 gives them, and require exactly the keys the parser requires.
func TestManifestSchema_RequirementAndResourceCarryExactlyTheirKeys(t *testing.T) {
	doc := schemaDoc(t)
	cases := []struct {
		list       string
		properties []string
		required   []string
	}{
		{"requires", []string{"in", "kind", "many", "name"}, []string{"kind"}},
		{"resources", []string{"inputs", "name", "on", "type"}, []string{"name", "type"}},
	}
	for _, tc := range cases {
		t.Run(tc.list, func(t *testing.T) {
			item := schemaAt(t, doc, doc, "properties", "spec", "properties", tc.list, "items")
			props, _ := item["properties"].(map[string]any)
			if got := sortedKeys(props); !reflect.DeepEqual(got, tc.properties) {
				t.Errorf("spec.%s items carry properties %q, want exactly %q", tc.list, got, tc.properties)
			}
			if got := sortedStrings(item["required"]); !reflect.DeepEqual(got, tc.required) {
				t.Errorf("spec.%s items require %q, want %q", tc.list, got, tc.required)
			}
			if item["additionalProperties"] != false {
				t.Errorf("spec.%s items additionalProperties = %v, want false", tc.list, item["additionalProperties"])
			}
		})
	}
}
