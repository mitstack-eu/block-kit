package main

// manifest_parser_test.go pins that the manifest suite reads the target's
// and the base's manifest through contract.ParseManifest, the kit's one
// manifest parser, and that manifest.go keeps no YAML reader of its own:
//
//   - a mitstack/v1beta manifest, ADR 0083's worked plain block, passes;
//   - a manifest the kit's parser refuses fails the suite with the parser's
//     reason, whether it is the target or the base;
//   - manifest.go declares nothing named for YAML, imports no package a
//     text reader is built from, scans no characters and calls
//     contract.ParseManifest.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// plainV1Beta returns ADR 0083's worked plain manifest from the kit's
// embedded manifest goldens.
func plainV1Beta(t *testing.T) string {
	t.Helper()
	raw, err := contract.ManifestGoldens.ReadFile(contract.ManifestGoldensDir + "/plain-v1beta.yaml")
	if err != nil {
		t.Fatalf("reading plain-v1beta.yaml from contract.ManifestGoldens: %v", err)
	}
	return string(raw)
}

func TestManifestSuite_AWorkedV1BetaManifestPasses(t *testing.T) {
	t.Run("no base manifest", func(t *testing.T) {
		requireManifestPass(t, manifestSuite(manifestDir(t, plainV1Beta(t)), ""))
	})
	t.Run("against an older v1alpha base", func(t *testing.T) {
		base := baseManifestFile(t, withVersion("2.4.0"))
		requireManifestPass(t, manifestSuite(manifestDir(t, plainV1Beta(t)), base))
	})
}

func TestManifestSuite_FailsAManifestTheKitParserRefuses(t *testing.T) {
	cases := []struct {
		label   string
		content string
		wants   []string
	}{
		{
			label:   "a v1beta manifest carrying spec.outputs",
			content: plainV1Beta(t) + "  outputs: [host]\n",
			wants:   []string{"manifest.yaml", "spec.outputs"},
		},
		{
			label:   "an unknown apiVersion",
			content: strings.Replace(manifestHead, "apiVersion: mitstack/v1alpha", "apiVersion: mitstack/v2", 1),
			wants:   []string{"manifest.yaml", "mitstack/v2", contract.ManifestAPIVersionV1Alpha, contract.ManifestAPIVersion},
		},
		{
			label:   "no apiVersion",
			content: strings.Replace(manifestHead, "apiVersion: mitstack/v1alpha\n", "", 1),
			wants:   []string{"manifest.yaml", contract.ManifestAPIVersionV1Alpha, contract.ManifestAPIVersion},
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if tc.content == manifestHead || tc.content == plainV1Beta(t) {
				t.Fatal("the fixture edit changed nothing")
			}
			requireManifestFail(t, manifestSuite(manifestDir(t, tc.content), ""), tc.wants...)
		})
	}

	t.Run("a base manifest under an unknown apiVersion", func(t *testing.T) {
		base := baseManifestFile(t, strings.Replace(withVersion("1.0.0"),
			"apiVersion: mitstack/v1alpha", "apiVersion: mitstack/v2", 1))
		target := withVersion("1.1.0") + "  releaseClass: feature\n  releaseNote: Adds the reads declaration.\n"
		requireManifestFail(t, manifestSuite(manifestDir(t, target), base),
			base, "mitstack/v2", contract.ManifestAPIVersion)
	})
}

// readerImports are the packages a text reader is built from, none of which
// the manifest suite needs once the kit parses the manifest.
var readerImports = map[string]bool{
	"bufio": true, "bytes": true, "regexp": true, "strconv": true,
	"text/scanner": true, "unicode": true, "unicode/utf8": true,
}

// readerStringsFuncs are the strings functions that cut text into lines,
// tokens or columns.
var readerStringsFuncs = map[string]bool{
	"Count": true, "Cut": true, "CutPrefix": true, "CutSuffix": true,
	"Fields": true, "FieldsFunc": true, "FieldsFuncSeq": true, "FieldsSeq": true,
	"Index": true, "IndexAny": true, "IndexByte": true, "IndexFunc": true, "IndexRune": true,
	"LastIndex": true, "LastIndexAny": true, "LastIndexByte": true, "LastIndexFunc": true,
	"Lines": true, "NewReader": true,
	"Split": true, "SplitAfter": true, "SplitAfterN": true, "SplitAfterSeq": true, "SplitN": true, "SplitSeq": true,
	"TrimFunc": true, "TrimLeft": true, "TrimLeftFunc": true, "TrimRight": true, "TrimRightFunc": true,
}

// A reader of YAML compares characters, cuts lines and keeps a cursor, so
// manifest.go is held to none of that: it declares nothing named for YAML,
// no method, no character literal, imports no package a reader is built
// from, calls no strings function that cuts text, and hands the manifest to
// contract.ParseManifest.
func TestManifestGo_HoldsNoYAMLReaderOfItsOwn(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "manifest.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing manifest.go: %v", err)
	}

	var problems []string
	at := func(n ast.Node) string { return fset.Position(n.Pos()).String() }
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if readerImports[path] {
			problems = append(problems, at(imp)+": imports "+path)
		}
	}
	callsParser := false
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Recv != nil {
				problems = append(problems, at(n)+": declares the method "+n.Name.Name)
			}
		case *ast.Ident:
			if strings.Contains(strings.ToLower(n.Name), "yaml") {
				problems = append(problems, at(n)+": names "+n.Name)
			}
		case *ast.BasicLit:
			if n.Kind == token.CHAR {
				problems = append(problems, at(n)+": compares against the character "+n.Value)
			}
		case *ast.SelectorExpr:
			pkg, ok := n.X.(*ast.Ident)
			if !ok {
				break
			}
			if pkg.Name == "strings" && readerStringsFuncs[n.Sel.Name] {
				problems = append(problems, at(n)+": calls strings."+n.Sel.Name)
			}
			if pkg.Name == "contract" && n.Sel.Name == "ParseManifest" {
				callsParser = true
			}
		}
		return true
	})
	if !callsParser {
		problems = append(problems, "manifest.go never calls contract.ParseManifest")
	}
	for _, p := range problems {
		t.Error(p)
	}
}
