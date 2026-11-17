package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// mitstackPackagePrefix marks the packages mitstack and its builders publish, whose GitHub reads the rule follows into.
const mitstackPackagePrefix = "github.com/mitstack-eu/"

// ghapiPackage defines ghapi.Base, the allowlist the rule asks a consumer to call, so it is the guard and never judged as a consumer.
const ghapiPackage = kitModulePath + "/capabilities/github/ghapi"

// listedPackage is the part of one `go list -json` record the rule reads.
type listedPackage struct {
	ImportPath     string
	Name           string
	Dir            string
	GoFiles        []string
	CgoFiles       []string
	IgnoredGoFiles []string
	Module         *struct {
		Path string
		Main bool
	}
	Error *struct {
		Err string
	}
}

// checkImportedGitHubBases judges each imported mitstack-eu package that reads the GitHub connection where that read lives, and answers an empty detail when none does.
func checkImportedGitHubBases(target string) (detail string, ok bool) {
	packages, err := importedMitstackPackages(target)
	if err != nil {
		return fmt.Sprintf("could not scan the packages %s imports for GitHub API use: %v", target, err), false
	}
	var verdicts []string
	for _, p := range packages {
		sources, err := packageSources(p)
		if err != nil {
			return fmt.Sprintf("could not scan %s, which %s imports, for GitHub API use: %v", p.ImportPath, target, err), false
		}
		read, offset, found := firstGitHubRead(sources)
		if !found {
			continue
		}
		consumer := fmt.Sprintf("%s reads the GitHub connection at %s/%s:%d",
			enclosingFunction(p.Name, read, offset), p.ImportPath, read.rel, lineAt(read.content, offset))
		verdict, ok := judgeGitHubBase(p.ImportPath, sources)
		if !ok {
			return consumer + ", and " + verdict, false
		}
		verdicts = append(verdicts, consumer+", "+verdict)
	}
	return strings.Join(verdicts, ", "), true
}

// importedMitstackPackages asks the Go toolchain for the packages target's non-test code imports, directly or not, and keeps the mitstack-eu ones outside target's own module.
func importedMitstackPackages(target string) ([]listedPackage, error) {
	cmd := exec.Command("go", "list", "-e", "-deps", "-json=ImportPath,Name,Dir,GoFiles,CgoFiles,IgnoredGoFiles,Module,Error", "./...")
	cmd.Dir = target
	// A go.work around target would make each module it uses a main module, which the rule skips as target's own.
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var kept []listedPackage
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		err := decoder.Decode(&p)
		if errors.Is(err, io.EOF) {
			return kept, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading go list output: %w", err)
		}
		judged, err := judgedImport(p)
		if err != nil {
			return nil, err
		}
		if judged {
			kept = append(kept, p)
		}
	}
}

// judgedImport refuses an unloadable mitstack-eu package, because a package the toolchain could not load is never evidence that the rule holds.
func judgedImport(p listedPackage) (bool, error) {
	if !strings.HasPrefix(p.ImportPath, mitstackPackagePrefix) || p.ImportPath == ghapiPackage {
		return false, nil
	}
	if p.Module != nil && p.Module.Main {
		return false, nil
	}
	if p.Error != nil {
		return false, fmt.Errorf("%s could not be loaded: %s", p.ImportPath, p.Error.Err)
	}
	return true, nil
}

// packageSources reads the files a build constraint leaves out as well, so a read that ships on another platform or tag is judged too.
func packageSources(p listedPackage) ([]goSource, error) {
	var sources []goSource
	for _, name := range slices.Concat(p.GoFiles, p.CgoFiles, p.IgnoredGoFiles) {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p.Dir, name))
		if err != nil {
			return nil, err
		}
		sources = append(sources, goSource{rel: name, content: string(raw)})
	}
	return sources, nil
}

func lineAt(content string, offset int) int {
	return strings.Count(content[:offset], "\n") + 1
}

// enclosingFunction names the function holding offset as package.Func or package.Type.Method, else the package alone, so a reader finds the read by name.
func enclosingFunction(pkgName string, s goSource, offset int) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, s.rel, s.content, parser.SkipObjectResolution)
	if err != nil {
		return "package " + pkgName
	}
	pos := fset.File(f.Pos()).Pos(offset)
	for _, decl := range f.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if isFunc && fn.Pos() <= pos && pos < fn.End() {
			return pkgName + "." + receiverPrefix(fn) + fn.Name.Name
		}
	}
	return "package " + pkgName
}

func receiverPrefix(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	typ := fn.Recv.List[0].Type
	if star, isPointer := typ.(*ast.StarExpr); isPointer {
		typ = star.X
	}
	if ident, named := typ.(*ast.Ident); named {
		return ident.Name + "."
	}
	return ""
}
