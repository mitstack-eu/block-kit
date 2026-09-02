package hostexec_test

// The package doc comment already states, for Run, that the ssm driver's
// script is recorded in CloudTrail (an inherent AWS-RunShellScript property,
// not a shortcut this package took) and that a caller must therefore never
// embed a secret in the script itself. RunOutput sends the script the exact
// same way — sendSSMCommand is shared between the two calls — so a reader who
// finds RunOutput without first reading Run's doc comment must not be able to
// miss that constraint.

import (
	"go/doc"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// hostexecDocPackage parses this directory's non-test .go files and returns
// their go/doc package, so a test can inspect an exported symbol's doc
// comment the same way godoc would render it.
func hostexecDocPackage(t *testing.T) *doc.Package {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing hostexec sources: %v", err)
	}
	pkg, ok := pkgs["hostexec"]
	if !ok {
		t.Fatal(`no "hostexec" package found among the non-test .go files in this directory`)
	}
	return doc.New(pkg, "", doc.AllDecls)
}

// TestRunOutputDocCarriesTheSameCloudTrailConstraintAsRun pins that RunOutput
// exists as an exported func at all (MIT-2507's first acceptance criterion —
// the kit exports one output-returning call beside Run) and that its own doc
// comment restates the CloudTrail constraint, rather than leaving a reader of
// RunOutput alone to assume it is silent the way the ssh driver is.
func TestRunOutputDocCarriesTheSameCloudTrailConstraintAsRun(t *testing.T) {
	docPkg := hostexecDocPackage(t)

	var runOutput *doc.Func
	for _, fn := range docPkg.Funcs {
		if fn.Name == "RunOutput" {
			runOutput = fn
			break
		}
	}
	if runOutput == nil {
		t.Fatal(`no exported func "RunOutput" found in package hostexec — the kit must export one output-returning call beside Run`)
	}
	if !strings.Contains(runOutput.Doc, "CloudTrail") {
		t.Errorf("RunOutput doc comment = %q, want it to name CloudTrail — the ssm driver records the script in CloudTrail on RunOutput exactly as it does on Run, since sendSSMCommand is shared between them, and that constraint must be stated on RunOutput itself, not left implicit", runOutput.Doc)
	}
}
