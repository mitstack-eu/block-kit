package kittest

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// declaresAbsenceProbe reports whether manifestYAML declares
// spec.lifecycle.absenceProbe: true. The kit module has no third-party
// dependencies, so this reads the manifest as plain text rather than
// parsing YAML: the declaration is a line whose trimmed text is exactly
// "absenceProbe: true", anywhere in the document.
func declaresAbsenceProbe(manifestYAML []byte) bool {
	for _, line := range strings.Split(string(manifestYAML), "\n") {
		if strings.TrimSpace(line) == "absenceProbe: true" {
			return true
		}
	}
	return false
}

// CheckAbsenceProbe reports whether an operator honours its own absence
// declaration: a manifest that declares spec.lifecycle.absenceProbe: true
// must answer validate for a torn-down resource with an "absent" boolean.
// It returns one problem per violation; an undeclared manifest owes nothing.
func CheckAbsenceProbe(manifestYAML []byte, validateReplyWhenAbsent []byte) []string {
	if !declaresAbsenceProbe(manifestYAML) {
		return nil
	}

	var doc map[string]any
	if err := json.Unmarshal(validateReplyWhenAbsent, &doc); err != nil {
		return []string{fmt.Sprintf("manifest declares spec.lifecycle.absenceProbe: true, but the validate reply for a torn-down resource is not a JSON object: %v", err)}
	}

	v, present := doc["absent"]
	if !present {
		return []string{`manifest declares spec.lifecycle.absenceProbe: true, but the validate reply for a torn-down resource carries no "absent" key`}
	}
	if _, ok := v.(bool); !ok {
		return []string{fmt.Sprintf(`manifest declares spec.lifecycle.absenceProbe: true, but "absent" is not a boolean, got %T`, v)}
	}
	return nil
}

// AssertAbsenceProbe fails t with one error per problem CheckAbsenceProbe finds.
func AssertAbsenceProbe(t *testing.T, manifestYAML []byte, validateReplyWhenAbsent []byte) {
	t.Helper()
	for _, problem := range CheckAbsenceProbe(manifestYAML, validateReplyWhenAbsent) {
		t.Error(problem)
	}
}
