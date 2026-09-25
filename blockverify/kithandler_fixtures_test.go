package main

// kithandler_fixtures_test.go holds blockverify's kit/v1 handler-import check
// to the shared fixture set in ../kitbinding/testdata/handler-import, the
// same set the platform's operator gates are held to. Each txtar file is a
// block source tree whose comment section opens with the verdict:
//
//   - pass: both kit/v1 suites pass and name the importing file;
//   - absent: both fail, naming the expected import, and neither reports a
//     scan that could not run;
//   - tooling: both fail as a scan that could not run, naming the file it
//     could not read, never as an absent import.

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const sharedHandlerFixtureDir = "../kitbinding/testdata/handler-import"

// scanFailureMarker is the phrase checkKitHandlerImport's tooling failure
// carries and its absent-import failure does not.
const scanFailureMarker = "could not scan"

type handlerFixture struct {
	name    string
	verdict string
	names   string
	files   map[string]string
}

func loadHandlerFixtures(t *testing.T) []handlerFixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(sharedHandlerFixtureDir, "*.txtar"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no fixtures under %s: the shared handler-import set is missing", sharedHandlerFixtureDir)
	}
	sort.Strings(paths)
	var out []handlerFixture
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f := handlerFixture{name: strings.TrimSuffix(filepath.Base(p), ".txtar"), files: map[string]string{}}
		var current string
		var body strings.Builder
		flush := func() {
			if current != "" {
				f.files[current] = body.String()
			}
			body.Reset()
		}
		sc := bufio.NewScanner(strings.NewReader(string(raw)))
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "-- ") && strings.HasSuffix(line, " --") && len(line) > 6 {
				flush()
				current = strings.TrimSuffix(strings.TrimPrefix(line, "-- "), " --")
				continue
			}
			if current == "" {
				if v, ok := strings.CutPrefix(line, "verdict: "); ok && f.verdict == "" {
					f.verdict = v
				}
				if v, ok := strings.CutPrefix(line, "names: "); ok && f.names == "" {
					f.names = v
				}
				continue
			}
			body.WriteString(line)
			body.WriteString("\n")
		}
		flush()
		if f.verdict == "" {
			t.Fatalf("%s has no verdict line", p)
		}
		out = append(out, f)
	}
	return out
}

// kitV1FixtureFilesFrom is the kit/v1 block of kitV1FixtureFiles with its
// Go sources replaced by the fixture's tree.
func kitV1FixtureFilesFrom(t *testing.T, f handlerFixture) map[string]string {
	t.Helper()
	files := kitV1FixtureFiles(t)
	delete(files, "main.go")
	for rel, content := range f.files {
		files[filepath.FromSlash(rel)] = content
	}
	return files
}

func TestKitV1_SharedHandlerFixtures_BothSuitesReachTheFixtureVerdict(t *testing.T) {
	for _, f := range loadHandlerFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			dir := newFixtureFrom(t, kitV1FixtureFilesFrom(t, f))

			for _, s := range []SuiteResult{trustPlumbingSuite(dir), contractSuite(dir)} {
				switch f.verdict {
				case "pass":
					if s.Status != "pass" {
						t.Errorf("%s status = %q, want %q: %s imports %s in code a default build compiles; detail: %s",
							s.Name, s.Status, "pass", f.names, kitHandlerImport, s.Detail)
						continue
					}
					if !strings.Contains(s.Detail, f.names) {
						t.Errorf("%s detail = %q, want it to name the importing file %s", s.Name, s.Detail, f.names)
					}
				case "absent":
					if s.Status != "fail" {
						t.Errorf("%s status = %q, want %q: no file a default build compiles imports %s; detail: %s",
							s.Name, s.Status, "fail", kitHandlerImport, s.Detail)
						continue
					}
					if !strings.Contains(s.Detail, kitHandlerImport) {
						t.Errorf("%s detail = %q, want it to name the expected import %s", s.Name, s.Detail, kitHandlerImport)
					}
					if strings.Contains(s.Detail, scanFailureMarker) {
						t.Errorf("%s detail = %q reports a scan that could not run, want an absent import", s.Name, s.Detail)
					}
				case "tooling":
					if s.Status != "fail" {
						t.Errorf("%s status = %q, want %q: the scan could not read %s; detail: %s",
							s.Name, s.Status, "fail", f.names, s.Detail)
						continue
					}
					if !strings.Contains(s.Detail, scanFailureMarker) {
						t.Errorf("%s detail = %q, want a tooling failure saying it %s, not an absent import",
							s.Name, s.Detail, scanFailureMarker)
					}
					if !strings.Contains(s.Detail, f.names) {
						t.Errorf("%s detail = %q, want it to name %s, the file the scan could not read", s.Name, s.Detail, f.names)
					}
				default:
					t.Fatalf("fixture %s: unknown verdict %q", f.name, f.verdict)
				}
			}
		})
	}
}

// A directory the scan cannot enter fails both suites as a tooling failure,
// even beside a readable file that imports the handler.
func TestKitV1_UnreadableDirectory_FailsBothSuitesAsAToolingFailure(t *testing.T) {
	files := kitV1FixtureFiles(t)
	files[filepath.Join("internal", "wire", "wire.go")] = "package wire\n"
	dir := newFixtureFrom(t, files)
	locked := filepath.Join(dir, "internal")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Fatalf("setup: %s is still readable after chmod 0 (running as root?), so the fault never reaches the scan", locked)
	}

	for _, s := range []SuiteResult{trustPlumbingSuite(dir), contractSuite(dir)} {
		if s.Status != "fail" {
			t.Errorf("%s status = %q, want %q: part of the block could not be read; detail: %s", s.Name, s.Status, "fail", s.Detail)
			continue
		}
		if !strings.Contains(s.Detail, scanFailureMarker) {
			t.Errorf("%s detail = %q, want a tooling failure saying it %s", s.Name, s.Detail, scanFailureMarker)
		}
	}
}
