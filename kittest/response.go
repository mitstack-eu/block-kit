package kittest

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// respGoldenEntries lists the embedded response golden files, failing the
// test rather than skipping: a harness that silently finds nothing proves
// nothing.
//
// The response goldens (MIT-1136) live in the contract package (MIT-3068,
// ADR 0068 decision 5: this repository owns them), which the engine's own
// contract tests also import. Embedding there rather than resolving a path
// relative to the caller's working directory is what keeps this usable
// from an operator living in a genuinely external repository, exactly as
// it does for the request-side goldens above.
func respGoldenEntries(t *testing.T) []fs.DirEntry {
	t.Helper()
	entries, err := fs.ReadDir(contract.ResponseGoldens, contract.ResponseGoldensDir)
	if err != nil {
		t.Fatalf("kittest: cannot read embedded response contract goldens: %v\n"+
			"The goldens are the contract; a conformance test that cannot find them proves nothing.", err)
	}
	if len(entries) == 0 {
		t.Fatalf("kittest: embedded response contract goldens are empty — this harness would pass vacuously")
	}
	return entries
}

func readRespGolden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := contract.ResponseGoldens.ReadFile(contract.ResponseGoldensDir + "/" + name)
	if err != nil {
		t.Fatalf("kittest: reading embedded response golden %q: %v", name, err)
	}
	return raw
}

// ValidResponseGoldens returns the response goldens representing well-formed
// operator responses the engine's decoder must read intact. Names starting
// with "invalid-" are excluded.
func ValidResponseGoldens(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, e := range respGoldenEntries(t) {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, "invalid-") {
			continue
		}
		out[strings.TrimSuffix(name, ".json")] = readRespGolden(t, name)
	}
	if len(out) == 0 {
		t.Fatal("kittest: no valid response goldens found — this harness would pass vacuously")
	}
	return out
}

// InvalidResponseGoldens returns the response goldens the engine's decoder
// cannot read the way an operator author would expect — the malformed set
// CheckResponse must reject.
func InvalidResponseGoldens(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, e := range respGoldenEntries(t) {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || !strings.HasPrefix(name, "invalid-") {
			continue
		}
		out[strings.TrimSuffix(name, ".json")] = readRespGolden(t, name)
	}
	if len(out) == 0 {
		t.Fatal("kittest: no invalid response goldens found — the rejection half of this harness would pass vacuously")
	}
	return out
}

// CheckResponse is a hand-written structural check over the decoded JSON
// body an operator's webhook handler wrote, mirroring
// api/internal/lifecycle/contract/response.schema.json without depending on
// a JSON-Schema library (the kit module has no third-party dependencies).
// It returns one human-readable problem per offending top-level property —
// an empty slice means the response conforms — and every problem string
// names the property so an operator author can act on it without reading
// anything else.
//
// An unknown top-level property is accepted, never flagged: the contract is
// additive-friendly (mirrors additionalProperties:true in the schema), so a
// new field either side adds ahead of the other can never fail a shipping
// operator's test.
func CheckResponse(raw []byte) []string {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return []string{fmt.Sprintf("response body is not a JSON object: %v", err)}
	}

	var problems []string

	checkStringMap := func(field string) {
		v, present := doc[field]
		if !present || v == nil {
			return // absent or explicit null — tolerated (a Go nil map encodes to null)
		}
		obj, ok := v.(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("%q must be an object of string values (or null), got %T", field, v))
			return
		}
		for k, ev := range obj {
			if _, ok := ev.(string); !ok {
				problems = append(problems, fmt.Sprintf("%q.%q must be a string value, got %T", field, k, ev))
			}
		}
	}

	checkBool := func(field string, nullable bool) {
		v, present := doc[field]
		if !present {
			return
		}
		if v == nil {
			if nullable {
				return
			}
			problems = append(problems, fmt.Sprintf("%q must be a boolean, got null", field))
			return
		}
		if _, ok := v.(bool); !ok {
			problems = append(problems, fmt.Sprintf("%q must be a boolean, got %T", field, v))
		}
	}

	checkString := func(field string) {
		v, present := doc[field]
		if !present || v == nil {
			return
		}
		if _, ok := v.(string); !ok {
			problems = append(problems, fmt.Sprintf("%q must be a string, got %T", field, v))
		}
	}

	checkStringMap("outputs")
	checkStringMap("secretOutputs")
	checkBool("pending", false)
	checkBool("absent", true)
	checkString("error")
	checkString("statusDetail")
	checkString("lastError")

	return problems
}

// AssertEmitsConformingResponses proves every response body an operator's
// webhook handler actually wrote conforms to the response contract, judged
// by CheckResponse. bodies is keyed by a caller-chosen label (typically the
// verb, or a scenario name for an error/pending path) purely for the
// sub-test name and failure message; the value is the exact bytes the
// handler wrote, never a struct re-encoded by the test — CheckResponse must
// see precisely what would go on the wire.
//
// An empty bodies map fails loudly rather than reporting green: a
// per-operator assertion that received nothing proves nothing about that
// operator's response shape, which is exactly the vacuous pass every other
// harness in this file exists to prevent.
func AssertEmitsConformingResponses(t *testing.T, bodies map[string][]byte) {
	t.Helper()
	if len(bodies) == 0 {
		t.Fatal("kittest: AssertEmitsConformingResponses received no response bodies — " +
			"an operator conformance test that captures nothing proves nothing about what this operator emits")
	}

	for label, raw := range bodies {
		t.Run(label, func(t *testing.T) {
			if problems := CheckResponse(raw); len(problems) != 0 {
				t.Errorf("response %q does not conform to the lifecycle webhook response contract: %v\n%s",
					label, problems, raw)
			}
		})
	}
}
