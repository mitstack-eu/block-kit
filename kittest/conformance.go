package kittest

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldensFS embeds the kit's own copy of the contract goldens (MIT-1585).
// api/internal/lifecycle/contract/goldens/ remains the SOURCE OF TRUTH; this
// is a committed, syncable mirror kept in lockstep by
// scripts/check-kittest-goldens-sync.sh in the required lint job. Embedding
// means every function in this file works from ANY caller working
// directory — a package inside this monorepo, a generated block in a temp
// dir, or an operator in a genuinely external repo importing this module.
//
//go:embed goldens/*.json
var goldensFS embed.FS

const goldensFSDir = "goldens"

// Conformance harness for the engine→operator webhook contract (MIT-1135).
//
// The contract is a set of committed golden envelopes in
// api/internal/lifecycle/contract/goldens/. The engine is tested to EMIT
// conforming payloads; the other half — that every operator ACCEPTS them, and
// rejects the malformed ones — was never enforced. `contract_golden_test.go`
// covered 3 of 13 operators and had no lint gate, and each operator hand-copied
// its own `request` struct, so "conforming" meant thirteen slightly different
// things.
//
// This is the operator-side half, as a helper an operator calls with its own
// decoder. It deliberately does NOT define the request type: an operator's
// struct is its own, and the contract is that it can DECODE the golden and see
// the fields it needs — not that it declares identical Go types. Requiring the
// latter would be a byte-parity gate on a struct, and MIT-1466 is the argument
// against that.

// goldenEntries lists the embedded golden files, failing the test rather
// than skipping: a conformance harness that silently finds nothing is the
// vacuous pass this package exists to prevent.
func goldenEntries(t *testing.T) []fs.DirEntry {
	t.Helper()
	entries, err := fs.ReadDir(goldensFS, goldensFSDir)
	if err != nil {
		t.Fatalf("kittest: cannot read embedded contract goldens: %v\n"+
			"The goldens are the contract; a conformance test that cannot find them proves nothing.", err)
	}
	if len(entries) == 0 {
		t.Fatalf("kittest: embedded contract goldens are empty — this harness would pass vacuously")
	}
	return entries
}

// readGolden reads one embedded golden file by its full name (with the
// .json suffix).
func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := goldensFS.ReadFile(goldensFSDir + "/" + name)
	if err != nil {
		t.Fatalf("kittest: reading embedded golden %q: %v", name, err)
	}
	return raw
}

// GoldensDir materializes the embedded contract goldens into a real
// directory (t.TempDir()) and returns its path. This is a shim kept for any
// caller reading the directory directly (os.ReadDir / os.ReadFile) rather
// than through Golden/ValidGoldens — it keeps working from any working
// directory, since MIT-1585 the fixtures no longer live on disk relative to
// the caller at all.
func GoldensDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, e := range goldenEntries(t) {
		if e.IsDir() {
			continue
		}
		raw := readGolden(t, e.Name())
		if err := os.WriteFile(filepath.Join(dir, e.Name()), raw, 0o600); err != nil {
			t.Fatalf("kittest: materializing embedded golden %s to %s: %v", e.Name(), dir, err)
		}
	}
	return dir
}

// Golden reads one golden envelope by name (without the .json suffix).
func Golden(t *testing.T, name string) []byte {
	t.Helper()
	return readGolden(t, name+".json")
}

// ValidGoldens returns the goldens representing well-formed envelopes an
// operator MUST accept. Names starting with "invalid-" are excluded.
func ValidGoldens(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, e := range goldenEntries(t) {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, "invalid-") {
			continue
		}
		out[strings.TrimSuffix(name, ".json")] = readGolden(t, name)
	}
	if len(out) == 0 {
		t.Fatal("kittest: no valid goldens found — this harness would pass vacuously")
	}
	return out
}

// AssertDecodesEveryValidGolden proves the operator can decode every well-formed
// envelope the engine emits, and that the fields it depends on survive the
// round trip.
//
// decode is the operator's own decoder (typically json.Unmarshal into its
// request type). require names the fields the operator must be able to see,
// checked against the golden's own JSON so a struct that silently drops one is
// caught — a field that decodes to the zero value looks identical to a field
// that was absent, which is how a contract drifts without anything failing.
func AssertDecodesEveryValidGolden(t *testing.T, decode func([]byte) error, require ...string) {
	t.Helper()

	for name, raw := range ValidGoldens(t) {
		t.Run(name, func(t *testing.T) {
			if err := decode(raw); err != nil {
				t.Fatalf("operator cannot decode the %s golden — the engine emits this: %v", name, err)
			}

			var envelope map[string]any
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatalf("golden %s is not valid JSON: %v", name, err)
			}
			for _, field := range require {
				if _, ok := envelope[field]; !ok {
					t.Errorf("golden %s has no %q field, but this operator requires it — "+
						"either the operator depends on something the contract does not promise, "+
						"or the golden is stale", name, field)
				}
			}
		})
	}
}

// AssertRejectsMalformed proves the operator refuses the envelopes the contract
// marks invalid. Accepting one is worse than failing to decode a valid one: it
// means the operator acts on a request the engine considers malformed.
func AssertRejectsMalformed(t *testing.T, accepts func([]byte) bool) {
	t.Helper()

	entries := goldenEntries(t)
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "invalid-") {
			continue
		}
		checked++
		raw := readGolden(t, name)
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			if accepts(raw) {
				t.Errorf("operator ACCEPTED %s — the contract marks this envelope invalid, "+
					"so acting on it means acting on a request the engine considers malformed", name)
			}
		})
	}
	if checked == 0 {
		t.Fatal("kittest: no invalid- goldens found — the rejection half of this harness would pass vacuously")
	}
}

// Describe renders a short summary of the goldens found, for a test that wants
// to log what it actually exercised rather than assert it silently.
func Describe(t *testing.T) string {
	t.Helper()
	valid := ValidGoldens(t)
	names := make([]string, 0, len(valid))
	for n := range valid {
		names = append(names, n)
	}
	return fmt.Sprintf("%d valid golden(s): %s", len(names), strings.Join(names, ", "))
}
