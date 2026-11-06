package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
)

const guardedClientTestNeedle = "func TestGuardedClient"

const guardedHTTPImport = kitModulePath + "/helpers/guardedhttp"

// rawNetworkUses lists, by import path, each name a capability operator must not use, and rawNetworkTypes each type it must not construct, since the kit's guarded client is the only way it reaches its vendor.
var (
	rawNetworkUses = map[string][]string{
		"net/http":   {"DefaultClient", "DefaultTransport", "Get", "Head", "Post", "PostForm"},
		"net":        {"Dial", "DialTimeout", "DialTCP", "DialUDP"},
		"crypto/tls": {"Dial", "DialWithDialer"},
	}
	rawNetworkTypes = map[string][]string{
		"net/http": {"Client", "Transport"},
		"net":      {"Dialer"},
	}
)

// declaresVerifyConnection reports whether the target's manifest lists verify-connection, and false for one that is absent or refused, which other suites report.
func declaresVerifyConnection(target string) bool {
	raw, err := os.ReadFile(filepath.Join(target, manifestFile))
	if err != nil {
		return false
	}
	m, err := contract.ParseManifest(raw)
	return err == nil && slices.Contains(m.Block.Spec.Lifecycle.Operations, contract.OperationVerifyConnection)
}

// guardedClientSuite holds a capability operator's outbound calls to the kit's guardedhttp client, and runs the TestGuardedClient that drives each guard against the client the operator builds.
func guardedClientSuite(target string) SuiteResult {
	const name = "guarded-client"

	if !declaresVerifyConnection(target) {
		return SuiteResult{name, "skipped", "the manifest does not list verify-connection, so the operator calls no vendor with an org's credential"}
	}
	if detail, ok := checkNoRawNetworkUse(target); !ok {
		return SuiteResult{name, "fail", detail}
	}
	if detail, ok := checkGuardedHTTPImport(target); !ok {
		return SuiteResult{name, "fail", detail}
	}
	if !strings.Contains(concatGoTestFiles(target), guardedClientTestNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf("%s is missing a %s... test, which calls kittest.AssertGuardedClient on the client the operator builds", target, guardedClientTestNeedle)}
	}
	ok, output := runGoTest(target, "TestGuardedClient")
	if !ok {
		return SuiteResult{name, "fail", detailFromGoTestOutput(output)}
	}
	return SuiteResult{name, "pass", "the operator calls its vendor through guardedhttp, and TestGuardedClient passes"}
}

// checkNoRawNetworkUse fails the first non-test Go file that builds an HTTP client, transport or dialer of its own, or uses the default ones.
func checkNoRawNetworkUse(target string) (detail string, ok bool) {
	var found string
	err := walkOperatorSources(target, func(path string, file *ast.File, fset *token.FileSet) {
		if found != "" {
			return
		}
		imported := map[string]string{}
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			local := filepath.Base(p)
			if imp.Name != nil {
				local = imp.Name.Name
			}
			imported[local] = p
		}
		report := func(sel *ast.SelectorExpr, what string) {
			rel, _ := filepath.Rel(target, path)
			found = fmt.Sprintf("%s:%d %s %s.%s, but a capability operator reaches its vendor through %s alone, which refuses a redirect and a loopback, private, link-local or unspecified address",
				rel, fset.Position(sel.Pos()).Line, what, sel.X.(*ast.Ident).Name, sel.Sel.Name, guardedHTTPImport)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if found != "" {
				return false
			}
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if rawName(imported, n, rawNetworkUses) {
					report(n, "uses")
				}
			case *ast.CompositeLit:
				if sel, isSel := n.Type.(*ast.SelectorExpr); isSel && rawName(imported, sel, rawNetworkTypes) {
					report(sel, "builds")
				}
			}
			return true
		})
	})
	if err != nil {
		return fmt.Sprintf("could not scan %s for raw network use: %v", target, err), false
	}
	return found, found == ""
}

// rawName reports whether sel names one of the watched members of a package the file imports.
func rawName(imported map[string]string, sel *ast.SelectorExpr, watched map[string][]string) bool {
	recv, isIdent := sel.X.(*ast.Ident)
	if !isIdent {
		return false
	}
	path, ok := imported[recv.Name]
	return ok && slices.Contains(watched[path], sel.Sel.Name)
}

func checkGuardedHTTPImport(target string) (detail string, ok bool) {
	imported := false
	err := walkOperatorSources(target, func(_ string, file *ast.File, _ *token.FileSet) {
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == guardedHTTPImport {
				imported = true
			}
		}
	})
	if err != nil {
		return fmt.Sprintf("could not scan %s for the %s import: %v", target, guardedHTTPImport, err), false
	}
	if !imported {
		return fmt.Sprintf("no non-test .go file under %s imports %s, so the operator's client is not the kit's", target, guardedHTTPImport), false
	}
	return "", true
}

func walkOperatorSources(target string, visit func(path string, file *ast.File, fset *token.FileSet)) error {
	return filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "vendor" || d.Name() == "testdata" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		visit(path, file, fset)
		return nil
	})
}
