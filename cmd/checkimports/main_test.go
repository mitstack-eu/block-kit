package main

// checkimports refuses a module's import of the core module or of a module whose manifest.yaml says type: block or type: capability, naming the import, its file and the fix, and passes every other import.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a module under scripts/testdata/import-check, which a block's CI reaches as this check does.
func fixture(name string) string {
	return filepath.Join("..", "..", "scripts", "testdata", "import-check", name)
}

func abs(t *testing.T, path string) string {
	t.Helper()
	dir, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// module writes a module named path whose go.mod replaces each dependency in deps with its directory, adds files beside it, and returns its directory.
func module(t *testing.T, path string, deps map[string]string, files map[string]string) string {
	t.Helper()
	t.Setenv("GOPROXY", "off")
	dir := t.TempDir()
	var mod strings.Builder
	fmt.Fprintf(&mod, "module %s\n\ngo 1.22\n", path)
	for dep, depDir := range deps {
		fmt.Fprintf(&mod, "\nrequire %s v0.0.0\n\nreplace %s => %q\n", dep, dep, depDir)
	}
	write(t, dir, map[string]string{"go.mod": mod.String()})
	write(t, dir, files)
	return dir
}

func goFile(pkg string, imports ...string) string {
	var src strings.Builder
	fmt.Fprintf(&src, "package %s\n\nimport (\n", pkg)
	for _, imp := range imports {
		fmt.Fprintf(&src, "\t_ %q\n", imp)
	}
	src.WriteString(")\n")
	return src.String()
}

func run(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = Run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func wantRefused(t *testing.T, stdout, stderr string, code int, wants ...string) {
	t.Helper()
	if code != 1 {
		t.Fatalf("exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	for _, want := range wants {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestRun_RefusesTheFixtureBlockThatImportsTheCoreModule(t *testing.T) {
	stdout, stderr, code := run(fixture("core-importer"))
	wantRefused(t, stdout, stderr, code,
		"main.go:7: imports github.com/mitstack/api/contract, a package of the core module github.com/mitstack/api.",
		"A kit or an operator never builds on the core: delete the import and reach mitstack over its HTTP API.")
}

func TestRun_RefusesTheFixtureBlockThatImportsAnotherBlocksModule(t *testing.T) {
	stdout, stderr, code := run(fixture("block-importer"))
	wantRefused(t, stdout, stderr, code,
		"main.go:7: imports example.com/other-block/outputs from example.com/other-block, whose manifest.yaml says type: block.",
		"A kit or an operator never builds on a block: delete the import, read another block's outcome from the outputs of a requirement your manifest declares, and move code two blocks share into a kit.")
}

func TestRun_PassesTheExampleBlock(t *testing.T) {
	stdout, stderr, code := run(filepath.Join("..", "..", "examples", "hetzner-firewall"))
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "none reaches the core module or a block's module") {
		t.Errorf("stdout lacks the pass line:\n%s", stdout)
	}
}

func TestRun_RefusesAnImportOfACapabilityWhoseTypeLineIsQuotedAndCommented(t *testing.T) {
	capability := module(t, "example.com/dns", nil, map[string]string{
		"manifest.yaml": "apiVersion: mitstack/v1beta\ntype: \"capability\" # serves every block\n",
		"zone/zone.go":  goFile("zone"),
	})
	dir := module(t, "example.com/app", map[string]string{"example.com/dns": capability}, map[string]string{"main.go": goFile("main", "example.com/dns/zone")})
	stdout, stderr, code := run(dir)
	wantRefused(t, stdout, stderr, code, "main.go:4: imports example.com/dns/zone from example.com/dns, whose manifest.yaml says type: capability.")
}

func TestRun_RefusesAnImportOfTheCoreModuleFromATestFile(t *testing.T) {
	dir := module(t, "example.com/app", map[string]string{"github.com/mitstack/api": abs(t, fixture("core"))}, map[string]string{
		"app.go":      goFile("app"),
		"app_test.go": goFile("app", "github.com/mitstack/api/contract"),
	})
	stdout, stderr, code := run(dir)
	wantRefused(t, stdout, stderr, code, "app_test.go:4: imports github.com/mitstack/api/contract, a package of the core module github.com/mitstack/api.")
}

func TestRun_ReportsEveryRefusedImportInOneRun(t *testing.T) {
	dir := module(t, "example.com/app", map[string]string{
		"github.com/mitstack/api": abs(t, fixture("core")),
		"example.com/other-block": abs(t, fixture("other-block")),
	}, map[string]string{
		"core.go":  goFile("app", "github.com/mitstack/api/contract"),
		"block.go": goFile("app", "example.com/other-block/outputs"),
	})
	stdout, stderr, code := run(dir)
	wantRefused(t, stdout, stderr, code,
		"core.go:4: imports github.com/mitstack/api/contract",
		"block.go:4: imports example.com/other-block/outputs",
		"checkimports: found 2 refused imports in 2 files of example.com/app.")
}

func TestRun_PassesAnImportThatReachesNeitherTheCoreNorABlock(t *testing.T) {
	for _, c := range []struct {
		name    string
		dep     string
		depFile map[string]string
		imp     string
	}{
		{"a module sharing the core module's path prefix", "github.com/mitstack/apiclient",
			map[string]string{"client/client.go": goFile("client")}, "github.com/mitstack/apiclient/client"},
		{"a module whose type line sits indented under another key", "example.com/tool",
			map[string]string{"manifest.yaml": "kind: tool\nspec:\n  type: block\n", "run/run.go": goFile("run")}, "example.com/tool/run"},
		{"a kit, whose root holds no manifest.yaml", "example.com/kit",
			map[string]string{"helpers/helpers.go": goFile("helpers")}, "example.com/kit/helpers"},
	} {
		t.Run(c.name, func(t *testing.T) {
			depDir := module(t, c.dep, nil, c.depFile)
			dir := module(t, "example.com/app", map[string]string{c.dep: depDir}, map[string]string{"main.go": goFile("main", c.imp, "fmt")})
			stdout, stderr, code := run(dir)
			if code != 0 {
				t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if want := "checkimports: judged 2 imports in 1 file of example.com/app, and none reaches the core module or a block's module"; !strings.Contains(stdout, want) {
				t.Errorf("stdout lacks %q:\n%s", want, stdout)
			}
		})
	}
}

func TestRun_PassesABlockImportingItsOwnPackages(t *testing.T) {
	dir := module(t, "example.com/self", nil, map[string]string{
		"manifest.yaml": "apiVersion: mitstack/v1beta\ntype: block\n",
		"main.go":       goFile("main", "example.com/self/outputs"),
		"outputs/o.go":  goFile("outputs"),
	})
	stdout, stderr, code := run(dir)
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

func TestRun_ExitsTwoNamingAnImportNoRequiredModuleProvides(t *testing.T) {
	dir := module(t, "example.com/app", nil, map[string]string{"main.go": goFile("main", "example.com/missing/pkg")})
	stdout, stderr, code := run(dir)
	if code != 2 {
		t.Fatalf("exit %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "main.go:4: cannot find the module of example.com/missing/pkg") {
		t.Errorf("stderr lacks the unresolved import:\n%s", stderr)
	}
}

func TestRun_ExitsTwoOnADirectoryHoldingNoModule(t *testing.T) {
	stdout, stderr, code := run(t.TempDir())
	if code != 2 {
		t.Fatalf("exit %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

func TestRun_ExitsTwoOnASecondDirectory(t *testing.T) {
	stdout, stderr, code := run(".", ".")
	if code != 2 || !strings.Contains(stderr, "judges one module") {
		t.Fatalf("exit %d, want 2 naming the one module it judges\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}
