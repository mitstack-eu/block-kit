package main

// run_test.go pins the CONTRACT for the `blockverify` command (MIT-1626 AC#3):
// a standalone conformance-suite runner an external repo's CI invokes via
// `go run github.com/mitstack-eu/operator-kit/blockverify@<tag>`.
//
// The seam under test is Run(args []string, stdout io.Writer) int — main()
// is expected to be nothing but `os.Exit(Run(os.Args[1:], os.Stdout))`. Every
// other symbol referenced below (Run itself) does not exist yet: this
// package does not compile until code-author adds it. That is the intended
// RED signal — see the test-author report for the confirmed `go vet` output.
//
// The report JSON shape is decoded into a LOCAL struct (below), not a
// production type, so this test pins the JSON CONTRACT (field names/shapes)
// without constraining code-author's internal Go type names.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reportSuite mirrors one entry of the report's "suites" array.
type reportSuite struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// report mirrors blockverify's standalone-mode JSON report — the same
// contract scripts/block-verify.sh's standalone mode already emits (see its
// header comment), now produced by this Go command instead of the shell
// script, so a downstream consumer (api/internal/conformance.Mint) can treat
// them identically.
type report struct {
	SuiteVersion   string        `json:"suiteVersion"`
	KitVersion     string        `json:"kitVersion"`
	Mode           string        `json:"mode"`
	ManifestDigest string        `json:"manifestDigest"`
	Verdict        string        `json:"verdict"`
	ResultsDigest  string        `json:"resultsDigest"`
	Suites         []reportSuite `json:"suites"`
}

// runBlockverify invokes the Run seam and captures its combined output.
func runBlockverify(t *testing.T, args ...string) (stdout string, exitCode int) {
	t.Helper()
	var buf bytes.Buffer
	exitCode = Run(args, &buf)
	return buf.String(), exitCode
}

// decodeReport parses stdout as a report, failing loudly (not skipping) if it
// isn't valid JSON — a --json invocation that emits anything else has already
// broken the contract every consumer of this output depends on.
func decodeReport(t *testing.T, stdout string) report {
	t.Helper()
	var r report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not a valid JSON report: %v\nstdout:\n%s", err, stdout)
	}
	return r
}

// expectedManifestDigest replicates the documented algorithm: sha256 of
// <dir>/manifest.yaml's raw bytes, formatted "sha256:<hex>".
func expectedManifestDigest(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		t.Fatalf("reading fixture manifest.yaml to compute the expected digest: %v", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// digestEntry is the {name,status} shape resultsDigest is computed over —
// field order matters, since it is byte-identical to
// `jq -c '[.suites[] | {name, status}]' | sha256sum`.
type digestEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// expectedResultsDigest recomputes resultsDigest from a report's own suites,
// in run order, EXCLUDING detail — pinning the exact digest algorithm.
func expectedResultsDigest(suites []reportSuite) string {
	entries := make([]digestEntry, 0, len(suites))
	for _, s := range suites {
		entries = append(entries, digestEntry{Name: s.Name, Status: s.Status})
	}
	raw, _ := json.Marshal(entries)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// suiteByName finds a suite result by name, failing the test if absent —
// every one of the 8 documented suites MUST always appear in the report,
// pass or fail or skipped.
func suiteByName(t *testing.T, r report, name string) reportSuite {
	t.Helper()
	for _, s := range r.Suites {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("report has no %q suite; suites present: %+v", name, r.Suites)
	return reportSuite{}
}

// --- 1. Golden pass -----------------------------------------------------

func TestRun_StandalonePass_GoldenFixture(t *testing.T) {
	dir := newGoldenFixture(t)

	stdout, exit := runBlockverify(t, "--json", "--standalone", dir)
	if exit != 0 {
		t.Fatalf("want exit 0 on a fully-conformant fixture, got %d\nstdout:\n%s", exit, stdout)
	}

	r := decodeReport(t, stdout)

	if r.Mode != "standalone" {
		t.Errorf("mode = %q, want %q", r.Mode, "standalone")
	}
	if r.SuiteVersion != "1.2.0" {
		t.Errorf("suiteVersion = %q, want %q (the suite set includes naming)", r.SuiteVersion, "1.2.0")
	}
	if r.KitVersion != "v0.1.0" {
		t.Errorf("kitVersion = %q, want %q (the kit pin floor)", r.KitVersion, "v0.1.0")
	}
	if r.Verdict != "pass" {
		t.Errorf("verdict = %q, want %q", r.Verdict, "pass")
	}

	wantManifestDigest := expectedManifestDigest(t, dir)
	if r.ManifestDigest != wantManifestDigest {
		t.Errorf("manifestDigest = %q, want %q (sha256 of <dir>/manifest.yaml)", r.ManifestDigest, wantManifestDigest)
	}
	if !strings.HasPrefix(r.ManifestDigest, "sha256:") {
		t.Errorf("manifestDigest = %q, want sha256:-prefixed", r.ManifestDigest)
	}

	wantResultsDigest := expectedResultsDigest(r.Suites)
	if r.ResultsDigest != wantResultsDigest {
		t.Errorf("resultsDigest = %q, want %q (sha256 over the compact [{name,status}] array)", r.ResultsDigest, wantResultsDigest)
	}

	wantOrder := []string{"trust-plumbing", "contract", "secret-safety", "teardown", "deliverable", "manifest", "naming"}
	if len(r.Suites) != len(wantOrder) {
		t.Fatalf("suites = %d entries, want %d: %+v", len(r.Suites), len(wantOrder), r.Suites)
	}
	for i, name := range wantOrder {
		if r.Suites[i].Name != name {
			t.Errorf("suites[%d].name = %q, want %q (run order is part of the contract)", i, r.Suites[i].Name, name)
		}
	}

	for _, name := range []string{"trust-plumbing", "contract", "secret-safety", "teardown", "deliverable", "manifest"} {
		s := suiteByName(t, r, name)
		if s.Status != "pass" {
			t.Errorf("suite %q status = %q, want %q; detail: %s", name, s.Status, "pass", s.Detail)
		}
	}

	naming := suiteByName(t, r, "naming")
	if naming.Status != "skipped" {
		t.Errorf("naming status = %q, want %q — the golden fixture declares no spec.naming, so no limit applies", naming.Status, "skipped")
	}
	if naming.Detail == "" {
		t.Error("naming must carry a reason in detail, not a silent skip")
	}

}

// --- 2. Failure modes -----------------------------------------------------

func TestRun_MissingManifestYaml_FailsTrustPlumbing(t *testing.T) {
	dir := t.TempDir()
	files := goldenFixtureFiles(t)
	delete(files, "manifest.yaml")
	writeFixture(t, dir, files)

	stdout, exit := runBlockverify(t, "--json", "--standalone", dir)
	if exit != 1 {
		t.Fatalf("want exit 1 (a target-specific problem, not a usage error) when manifest.yaml is missing, got %d\nstdout:\n%s", exit, stdout)
	}

	r := decodeReport(t, stdout)
	if r.Verdict != "fail" {
		t.Errorf("verdict = %q, want %q", r.Verdict, "fail")
	}
	tp := suiteByName(t, r, "trust-plumbing")
	if tp.Status != "fail" {
		t.Errorf("trust-plumbing status = %q, want %q", tp.Status, "fail")
	}
	if !strings.Contains(tp.Detail, "manifest.yaml") {
		t.Errorf("trust-plumbing detail = %q, want it to name manifest.yaml", tp.Detail)
	}
}

func TestRun_MissingDockerfile_FailsDeliverable(t *testing.T) {
	dir := t.TempDir()
	files := goldenFixtureFiles(t)
	delete(files, "Dockerfile")
	writeFixture(t, dir, files)

	stdout, exit := runBlockverify(t, "--json", "--standalone", dir)
	if exit != 1 {
		t.Fatalf("want exit 1 when Dockerfile is missing, got %d\nstdout:\n%s", exit, stdout)
	}

	r := decodeReport(t, stdout)
	if r.Verdict != "fail" {
		t.Errorf("verdict = %q, want %q", r.Verdict, "fail")
	}
	d := suiteByName(t, r, "deliverable")
	if d.Status != "fail" {
		t.Errorf("deliverable status = %q, want %q", d.Status, "fail")
	}
	if !strings.Contains(d.Detail, "Dockerfile") {
		t.Errorf("deliverable detail = %q, want it to name Dockerfile", d.Detail)
	}

}

func TestRun_RedeclaredKitOwnedSymbol_FailsTrustPlumbing(t *testing.T) {
	dir := t.TempDir()
	files := goldenFixtureFiles(t)
	// A second, non-shim, non-test file that redeclares a kit-owned symbol.
	// This makes the fixture module fail to COMPILE (kitshim.go already
	// defines EmitProgress) — which is fine: the trust-plumbing check is
	// static/textual (reimplementing the check-operator-kit-pin.sh greps) and
	// must catch this without needing the module to build.
	files["rogue.go"] = `package main

import "context"

func EmitProgress(ctx context.Context, detail string) error { return nil }
`
	writeFixture(t, dir, files)

	stdout, exit := runBlockverify(t, "--json", "--standalone", dir)
	if exit != 1 {
		t.Fatalf("want exit 1 when a kit-owned symbol is redeclared, got %d\nstdout:\n%s", exit, stdout)
	}

	r := decodeReport(t, stdout)
	if r.Verdict != "fail" {
		t.Errorf("verdict = %q, want %q", r.Verdict, "fail")
	}
	tp := suiteByName(t, r, "trust-plumbing")
	if tp.Status != "fail" {
		t.Errorf("trust-plumbing status = %q, want %q", tp.Status, "fail")
	}
	if !strings.Contains(tp.Detail, "EmitProgress") {
		t.Errorf("trust-plumbing detail = %q, want it to name the redeclared symbol EmitProgress", tp.Detail)
	}
}

func TestRun_MissingContractTests_FailsContract(t *testing.T) {
	dir := t.TempDir()
	files := goldenFixtureFiles(t)
	delete(files, "contract_conformance_test.go")
	writeFixture(t, dir, files)

	stdout, exit := runBlockverify(t, "--json", "--standalone", dir)
	if exit != 1 {
		t.Fatalf("want exit 1 when TestContract_* is missing, got %d\nstdout:\n%s", exit, stdout)
	}

	r := decodeReport(t, stdout)
	if r.Verdict != "fail" {
		t.Errorf("verdict = %q, want %q", r.Verdict, "fail")
	}
	c := suiteByName(t, r, "contract")
	if c.Status != "fail" {
		t.Errorf("contract status = %q, want %q — `go test -run TestContract_` with NO matching tests exits 0 (vacuous pass) unless presence is checked explicitly, exactly the gap check-operator-contract-tests.sh's grep guards against", c.Status, "fail")
	}
	if !strings.Contains(c.Detail, "TestContract_") {
		t.Errorf("contract detail = %q, want it to name the missing TestContract_* function(s)", c.Detail)
	}
}

// TestRun_MissingResponseContractTest_FailsContract is the response-side twin
// of the case above, and the behavioural half of contract_mirror_test.go's
// list comparison: that test proves blockverify KNOWS about
// TestContract_EmitsConformingResponse, this one proves it ACTS on it. Both
// are needed — a name present in contractRequiredTests but never reached by
// the presence check would satisfy the list comparison and still let a module
// with no response coverage through.
func TestRun_MissingResponseContractTest_FailsContract(t *testing.T) {
	dir := t.TempDir()
	files := goldenFixtureFiles(t)
	delete(files, "contract_response_test.go")
	writeFixture(t, dir, files)

	stdout, exit := runBlockverify(t, "--json", "--standalone", dir)
	if exit != 1 {
		t.Fatalf("want exit 1 when TestContract_EmitsConformingResponse is missing, got %d\nstdout:\n%s", exit, stdout)
	}

	r := decodeReport(t, stdout)
	if r.Verdict != "fail" {
		t.Errorf("verdict = %q, want %q", r.Verdict, "fail")
	}
	c := suiteByName(t, r, "contract")
	if c.Status != "fail" {
		t.Errorf("contract status = %q, want %q — the response half of the webhook contract is required of every operator (the engine's decoder discards its unmarshal error, so a non-conforming response loses the whole payload silently)", c.Status, "fail")
	}
	if !strings.Contains(c.Detail, "TestContract_EmitsConformingResponse") {
		t.Errorf("contract detail = %q, want it to name the missing TestContract_EmitsConformingResponse", c.Detail)
	}
}

// --- 3. Determinism ---------------------------------------------------

func TestRun_ResultsDigest_DeterministicAcrossRuns(t *testing.T) {
	dir := newGoldenFixture(t)

	stdout1, exit1 := runBlockverify(t, "--json", "--standalone", dir)
	if exit1 != 0 {
		t.Fatalf("first run: want exit 0, got %d\n%s", exit1, stdout1)
	}
	stdout2, exit2 := runBlockverify(t, "--json", "--standalone", dir)
	if exit2 != 0 {
		t.Fatalf("second run: want exit 0, got %d\n%s", exit2, stdout2)
	}

	r1 := decodeReport(t, stdout1)
	r2 := decodeReport(t, stdout2)

	if r1.ResultsDigest != r2.ResultsDigest {
		t.Errorf("resultsDigest is not deterministic across identical runs: %q vs %q", r1.ResultsDigest, r2.ResultsDigest)
	}
	if r1.ManifestDigest != r2.ManifestDigest {
		t.Errorf("manifestDigest is not deterministic across identical runs: %q vs %q", r1.ManifestDigest, r2.ManifestDigest)
	}
}

func TestRun_ResultsDigest_ChangesWhenASuiteOutcomeChanges(t *testing.T) {
	passDir := newGoldenFixture(t)
	passStdout, exit := runBlockverify(t, "--json", "--standalone", passDir)
	if exit != 0 {
		t.Fatalf("golden fixture: want exit 0, got %d\n%s", exit, passStdout)
	}
	passReport := decodeReport(t, passStdout)

	failDir := t.TempDir()
	files := goldenFixtureFiles(t)
	delete(files, "Dockerfile")
	writeFixture(t, failDir, files)
	failStdout, _ := runBlockverify(t, "--json", "--standalone", failDir)
	failReport := decodeReport(t, failStdout)

	if passReport.ResultsDigest == failReport.ResultsDigest {
		t.Errorf("resultsDigest must change when a suite's status changes (pass fixture vs missing-Dockerfile fixture): both were %q", passReport.ResultsDigest)
	}
}

// --- 4. Golden report fixture for the API side (see golden_test.go) is
//        tested separately.

// --- 5. repo-mode refusal exits 2 ---

func TestRun_RepoModeRefused_NoStandaloneFlag(t *testing.T) {
	dir := newGoldenFixture(t)

	// No --standalone flag at all — v1 supports ONLY standalone.
	stdout, exit := runBlockverify(t, dir)
	if exit != 2 {
		t.Fatalf("want exit 2 (usage/config error) refusing repo mode, got %d\nstdout:\n%s", exit, stdout)
	}
	if !strings.Contains(stdout, "scripts/block-verify.sh") {
		t.Errorf("refusal message must name scripts/block-verify.sh as the repo-mode tool, got:\n%s", stdout)
	}
}

func TestRun_RepoModeRefused_NoArgsAtAll(t *testing.T) {
	stdout, exit := runBlockverify(t)
	if exit != 2 {
		t.Fatalf("want exit 2 with no arguments at all, got %d\nstdout:\n%s", exit, stdout)
	}
	if !strings.Contains(stdout, "scripts/block-verify.sh") {
		t.Errorf("refusal message must name scripts/block-verify.sh, got:\n%s", stdout)
	}
}

func TestRun_UsageError_StandaloneFlagWithNoTarget(t *testing.T) {
	// --standalone IS supplied, but with no target directory: a genuine usage
	// error (distinct from a target-specific conformance failure), still 2.
	stdout, exit := runBlockverify(t, "--json", "--standalone")
	if exit != 2 {
		t.Fatalf("want exit 2 when --standalone has no target, got %d\nstdout:\n%s", exit, stdout)
	}
}
