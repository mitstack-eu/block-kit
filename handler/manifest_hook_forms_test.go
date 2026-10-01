package handler

import (
	"reflect"
	"testing"
)

// TestManifestHooks_ReadsQuotedAndCommentedFormsAsYAMLDoes pins the exact
// names manifestHooks reads for each quoted or commented form: the plain
// scalar YAML reads, with no quote characters and no comment text.
func TestManifestHooks_ReadsQuotedAndCommentedFormsAsYAMLDoes(t *testing.T) {
	const head = "spec:\n  layer: infra\n  lifecycle:\n"
	cases := []struct {
		name  string
		hooks string
		want  []string
	}{
		{"double-quoted sequence items", "    hooks:\n      - \"beforeEnsure\"\n      - \"afterDelete\"\n", []string{"beforeEnsure", "afterDelete"}},
		{"single-quoted sequence items", "    hooks:\n      - 'beforeEnsure'\n      - 'afterDelete'\n", []string{"beforeEnsure", "afterDelete"}},
		{"sequence item with an inline comment", "    hooks:\n      - beforeEnsure # pre\n      - afterDelete\n", []string{"beforeEnsure", "afterDelete"}},
		{"quoted sequence item with an inline comment", "    hooks:\n      - 'beforeEnsure'   # pre\n", []string{"beforeEnsure"}},
		{"comment after the hooks key", "    hooks: # the hook points\n      - beforeEnsure\n", []string{"beforeEnsure"}},
		{"flow list with quoted items", "    hooks: [\"beforeEnsure\", 'afterDelete']\n", []string{"beforeEnsure", "afterDelete"}},
		{"flow list with a quoted and a plain item", "    hooks: [\"beforeEnsure\", afterDelete]\n", []string{"beforeEnsure", "afterDelete"}},
		{"flow list followed by a trailing comment", "    hooks: [beforeEnsure] # c\n", []string{"beforeEnsure"}},
		{"empty flow list followed by a trailing comment", "    hooks: [] # none yet\n", nil},
		{"sequence at the key's own indent", "    hooks:\n    - beforeEnsure\n    - afterDelete\n    contract: kit/v1\n", []string{"beforeEnsure", "afterDelete"}},
		{"sequence at the key's own indent with a comment between items", "    hooks:\n    - beforeEnsure\n    # the delete side\n    - afterDelete\n", []string{"beforeEnsure", "afterDelete"}},
		{"comment at column 0 before the hooks key", "# note\n    hooks:\n      - beforeEnsure\n", []string{"beforeEnsure"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := manifestHooks([]byte(head + tc.hooks))
			if err != nil {
				t.Fatalf("manifestHooks: unexpected error: %v", err)
			}
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("manifestHooks = %q, want %q\nmanifest:\n%s", got, tc.want, head+tc.hooks)
			}
		})
	}
}

// TestManifestHooks_ACommentOnTheSpecLineDoesNotHideTheHooks pins that a
// trailing comment on the top-level spec: line still opens spec's block. The
// table above prepends a fixed spec: line, so this form needs its own test.
func TestManifestHooks_ACommentOnTheSpecLineDoesNotHideTheHooks(t *testing.T) {
	doc := "spec: # note\n  layer: infra\n  lifecycle:\n    hooks:\n      - beforeEnsure\n"
	got, err := manifestHooks([]byte(doc))
	if err != nil {
		t.Fatalf("manifestHooks: unexpected error: %v", err)
	}
	if want := []string{"beforeEnsure"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("manifestHooks = %q, want %q\nmanifest:\n%s", got, want, doc)
	}
}

// TestManifestHooks_HashInsideQuotesIsNotAComment pins that a "#" inside a
// quoted scalar is part of the value, as in YAML, so the name read keeps it
// and New's closed-set check, not the comment stripper, is what refuses it.
func TestManifestHooks_HashInsideQuotesIsNotAComment(t *testing.T) {
	doc := "spec:\n  layer: infra\n  lifecycle:\n    hooks:\n      - \"before#Ensure\"\n"
	got, err := manifestHooks([]byte(doc))
	if err != nil {
		t.Fatalf("manifestHooks: unexpected error: %v", err)
	}
	if want := []string{"before#Ensure"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("manifestHooks = %q, want %q", got, want)
	}
}
