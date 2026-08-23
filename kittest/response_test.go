package kittest

import (
	"strings"
	"testing"
)

// The response half of the lifecycle webhook contract (MIT-1136).
//
// The request direction has a schema, goldens and a per-operator conformance
// test. The response an operator emits had none of the three, so an operator
// could answer with a shape the engine mis-decodes and nothing anywhere would
// notice. The engine's decoder discards its unmarshal error, so a response
// whose outputs values are numbers rather than strings loses the whole payload
// in silence.
//
// CheckResponse is the operator-side judge of that contract. Every operator's
// TestContract_EmitsConformingResponse trusts it, so it is checked here first,
// against the same committed goldens the engine-side schema is checked
// against. A checker that accepts everything would make sixteen operator tests
// pass while proving nothing, which is the vacuous pass this package exists to
// prevent.

// invalidGoldenField maps each invalid- response golden to the top-level
// property whose type it breaks. The table is explicit rather than derived
// from the file name so that adding an invalid golden forces a decision about
// what its problem text must name, instead of quietly widening the set.
var invalidGoldenField = map[string]string{
	"invalid-outputs-not-strings":       "outputs",
	"invalid-outputs-not-object":        "outputs",
	"invalid-pending-not-bool":          "pending",
	"invalid-absent-not-bool":           "absent",
	"invalid-secret-outputs-not-strings": "secretOutputs",
}

func TestCheckResponse_AcceptsEveryValidResponseGolden(t *testing.T) {
	for name, raw := range ValidResponseGoldens(t) {
		t.Run(name, func(t *testing.T) {
			if problems := CheckResponse(raw); len(problems) != 0 {
				t.Errorf("CheckResponse rejected the %s response golden with %v — "+
					"the goldens ARE the contract, so a valid one the checker refuses "+
					"means sixteen shipping operators are about to be failed by a rule "+
					"the contract never made.\n%s", name, problems, raw)
			}
		})
	}
}

func TestCheckResponse_RejectsEveryInvalidResponseGoldenNamingTheOffendingField(t *testing.T) {
	invalid := InvalidResponseGoldens(t)

	for name, raw := range invalid {
		field, known := invalidGoldenField[name]
		if !known {
			t.Fatalf("invalid response golden %q has no expected field in invalidGoldenField — "+
				"a new malformed fixture must say which property its problem text has to name, "+
				"or this test starts accepting any complaint at all", name)
		}

		t.Run(name, func(t *testing.T) {
			problems := CheckResponse(raw)
			if len(problems) == 0 {
				t.Fatalf("CheckResponse ACCEPTED %s — the contract marks this response "+
					"malformed, and accepting it is how a payload the engine cannot decode "+
					"reaches the engine.\n%s", name, raw)
			}
			joined := strings.Join(problems, "; ")
			if !strings.Contains(joined, field) {
				t.Errorf("CheckResponse rejected %s but its problem text never names %q: %v\n"+
					"An operator author reads this string and nothing else; a complaint that "+
					"does not name the field is a complaint they cannot act on.", name, field, problems)
			}
		})
	}
}

func TestResponseGoldens_BothHalvesArePresent(t *testing.T) {
	valid := ValidResponseGoldens(t)
	invalid := InvalidResponseGoldens(t)

	if len(valid) == 0 {
		t.Fatal("ValidResponseGoldens returned nothing — the acceptance half of this harness would pass vacuously")
	}
	if len(invalid) == 0 {
		t.Fatal("InvalidResponseGoldens returned nothing — the rejection half of this harness would pass vacuously")
	}

	for name := range valid {
		if strings.HasPrefix(name, "invalid-") {
			t.Errorf("ValidResponseGoldens returned %q — names prefixed invalid- are the malformed set", name)
		}
	}
	for name := range invalid {
		if !strings.HasPrefix(name, "invalid-") {
			t.Errorf("InvalidResponseGoldens returned %q — the malformed set is the invalid- prefixed names only", name)
		}
	}

	// null-outputs is the fixture that keeps the contract additive: a Go nil
	// map encodes to JSON null, several shipping operators emit exactly that,
	// and the engine already tolerates it.
	if _, ok := valid["null-outputs"]; !ok {
		t.Error("the valid set is missing null-outputs, the fixture that pins a null outputs map as legitimate")
	}
}

func TestCheckResponse_AcceptsAValidResponseCarryingAnUnknownProperty(t *testing.T) {
	// A contract that rejects properties it has not heard of cannot be
	// extended without breaking every operator already in the field, so an
	// unknown top-level property is accepted rather than flagged.
	const withUnknown = `{"outputs":{"url":"https://app.example.com"},"somethingTheEngineWillAddLater":{"nested":1}}`

	if problems := CheckResponse([]byte(withUnknown)); len(problems) != 0 {
		t.Errorf("CheckResponse rejected a conforming response carrying an unknown property: %v\n%s",
			problems, withUnknown)
	}
}
