package main

// golden_test.go pins the golden-pass report as a checked-in fixture,
// standard `-update` golden pattern. This is how the cross-module round trip
// gets tested: the KIT module cannot import api/internal (and shouldn't —
// blockverify must work against a genuinely external repo with no access to
// this monorepo at all), so instead this file writes the exact report
// blockverify produces for the golden fixture to
// testdata/standalone-report.golden.json, and api/internal/conformance's
// round-trip test (on this branch: roundtrip_test.go) is expected to consume
// a COPY of this same file to prove `conformance.Mint` accepts exactly the
// shape this command emits. Wiring that copy is code-author's job — see the
// CONTRACT note in the test-author report.
//
// Until Run() exists, this file does not compile (same RED as run_test.go),
// so there is nothing to regenerate yet. Once code-author is green, populate
// the golden with:
//
//	cd examples/operator-kit/blockverify && \
//	  GOTOOLCHAIN=auto /home/mike/go-1.22/bin/go test -run TestGolden_StandaloneReport -update
//
// and commit testdata/standalone-report.golden.json.

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

const goldenReportPath = "testdata/standalone-report.golden.json"

func TestGolden_StandaloneReport(t *testing.T) {
	dir := newGoldenFixture(t)

	stdout, exit := runBlockverify(t, "--json", "--standalone", dir)
	if exit != 0 {
		t.Fatalf("golden fixture must pass: exit %d\n%s", exit, stdout)
	}

	// Re-marshal (rather than storing raw stdout bytes) so the golden file is
	// stable pretty-printed JSON regardless of whether Run's own output is
	// compact or indented.
	got := decodeReport(t, stdout)
	gotPretty, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("re-marshal report: %v", err)
	}

	path := filepath.Join(".", goldenReportPath)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, append(gotPretty, '\n'), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		t.Logf("updated golden %s — commit it, and copy it into api/internal/conformance's fixtures", path)
		return
	}

	wantRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file %s not found — run with -update once blockverify is implemented and passing: %v", path, err)
	}
	var want report
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatalf("golden file %s is not valid JSON: %v", path, err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("report does not match golden %s.\ngot:\n%s\nwant:\n%s\n\nIf this is an intentional shape change, re-run with -update and commit the result.",
			path, gotPretty, wantRaw)
	}
}
