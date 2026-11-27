package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const coreModule = "github.com/mitstack/api"

// listedPackage holds the fields of go list's JSON this check reads.
type listedPackage struct {
	ImportPath   string
	Dir          string
	Standard     bool
	ForTest      string
	GoFiles      []string
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
	Module       *struct {
		Path string
		Dir  string
		Main bool
	}
	Error *struct{ Err string }
}

// judgedModule is the judged module's own files, its root, and every package its build and tests reach, by import path.
type judgedModule struct {
	path, dir string
	files     []string
	reached   map[string]listedPackage
}

// listModule runs go list over every package of the module at dir and its tests, with GOWORK off so the module's own go.mod decides each dependency.
func listModule(dir string) (judgedModule, error) {
	cmd := exec.Command("go", "list", "-e", "-deps", "-test",
		"-json=ImportPath,Dir,Standard,ForTest,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,Module,Error", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return judgedModule{}, fmt.Errorf("go list in %s: %v\n%s", dir, err, strings.TrimSpace(errOut.String()))
	}
	mod := judgedModule{reached: map[string]listedPackage{}}
	seen := map[string]bool{}
	var unmatched []string
	dec := json.NewDecoder(&out)
	for {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return judgedModule{}, fmt.Errorf("reading go list's output in %s: %v", dir, err)
		}
		if p.ImportPath == "./..." && p.Error != nil {
			unmatched = append(unmatched, p.Error.Err)
		}
		path, _, _ := strings.Cut(p.ImportPath, " [")
		if _, ok := mod.reached[path]; !ok || mod.reached[path].Error != nil {
			mod.reached[path] = p
		}
		if p.Module == nil || !p.Module.Main || p.ForTest != "" || strings.HasSuffix(path, ".test") || path != p.ImportPath {
			continue
		}
		mod.path, mod.dir = p.Module.Path, p.Module.Dir
		for _, f := range slices.Concat(p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles) {
			if file := filepath.Join(p.Dir, f); !seen[file] {
				seen[file] = true
				mod.files = append(mod.files, file)
			}
		}
	}
	if mod.path == "" {
		if warning := strings.TrimSpace(errOut.String()); warning != "" {
			unmatched = append(unmatched, warning)
		}
		return judgedModule{}, fmt.Errorf("go list found no package of a Go module in %s, so nothing was judged: %s", dir, strings.Join(unmatched, "; "))
	}
	slices.Sort(mod.files)
	return mod, nil
}

type finding struct {
	file       string
	line       int
	importPath string
	module     string
	moduleType string
}

type unresolved struct {
	file       string
	line       int
	importPath string
	reason     string
}

type judgement struct {
	refused    []finding
	unresolved []unresolved
	imports    int
	files      int
}

// judge reads each import of the module's files and refuses one that reaches the core module or a block's module.
func judge(mod judgedModule) (judgement, error) {
	var j judgement
	types := map[string]string{}
	fset := token.NewFileSet()
	for _, file := range mod.files {
		parsed, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			return judgement{}, err
		}
		name := file
		if rel, err := filepath.Rel(mod.dir, file); err == nil {
			name = filepath.ToSlash(rel)
		}
		j.files++
		for _, spec := range parsed.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return judgement{}, fmt.Errorf("%s: reading import %s: %v", name, spec.Path.Value, err)
			}
			j.imports++
			line := fset.Position(spec.Pos()).Line
			if imp == coreModule || strings.HasPrefix(imp, coreModule+"/") {
				j.refused = append(j.refused, finding{file: name, line: line, importPath: imp, module: coreModule})
				continue
			}
			if imp == "C" {
				continue
			}
			p, ok := mod.reached[imp]
			switch {
			case ok && p.Standard:
				continue
			case !ok:
				j.unresolved = append(j.unresolved, unresolved{name, line, imp, "go list reached no package of that path"})
				continue
			case p.Module == nil || p.Module.Dir == "":
				reason := "go list names no module directory for it"
				if p.Error != nil {
					reason = p.Error.Err
				}
				j.unresolved = append(j.unresolved, unresolved{name, line, imp, reason})
				continue
			case p.Module.Main:
				continue
			}
			typ, ok := types[p.Module.Dir]
			if !ok {
				if typ, err = manifestType(p.Module.Dir); err != nil {
					return judgement{}, err
				}
				types[p.Module.Dir] = typ
			}
			if typ == "block" || typ == "capability" {
				j.refused = append(j.refused, finding{file: name, line: line, importPath: imp, module: p.Module.Path, moduleType: typ})
			}
		}
	}
	return j, nil
}

// manifestType returns the value of the first unindented type: line in the manifest.yaml at dir, or "" when dir holds no manifest.yaml or the file has no such line.
func manifestType(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		value, ok := strings.CutPrefix(line, "type:")
		if !ok {
			continue
		}
		value, _, _ = strings.Cut(value, " #")
		return strings.Trim(strings.TrimSpace(value), `"'`), nil
	}
	return "", nil
}
