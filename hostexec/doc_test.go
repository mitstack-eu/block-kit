package hostexec_test

// RunOutput sends the script exactly as Run does, so a reader who finds it without Run's doc must not miss the CloudTrail constraint.

import (
	"go/doc"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

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
