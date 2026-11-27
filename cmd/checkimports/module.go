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
	ImportPath     string
	Dir            string
	Standard       bool
	ForTest        string
	GoFiles        []string
	CgoFiles       []string
	TestGoFiles    []string
	XTestGoFiles   []string
	IgnoredGoFiles []string
	Module         *struct {
		Path string
		Dir  string
		Main bool
	}
	Error *struct{ Err string }
}

type judgedModule struct {
	path, dir string
	// files are relative to dir, and built holds those the default build selects.
	files   []string
	built   map[string]bool
	reached map[string]listedPackage
}

// findModules returns dir and every module nested under it, since go list's ./... stops at a nested go.mod.
func findModules(dir string) ([]string, error) {
	mods := []string{dir}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == dir {
			return err
		}
		if ignoredDir(d.Name()) {
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
			mods = append(mods, path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
	return mods, err
}

func ignoredDir(name string) bool {
	return name == "testdata" || name == "vendor" || ignoredName(name)
}

// ignoredName matches the names the go command ignores, for a file and a directory alike.
func ignoredName(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// moduleFiles lists the module's Go files itself, since go list names no file of a package whose every file the build constraints leave out.
func moduleFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == dir {
				return nil
			}
			if ignoredDir(d.Name()) {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") && !ignoredName(d.Name()) {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files, err
}

// listModule sets GOWORK off so the module's own go.mod decides each dependency.
func listModule(dir string) (judgedModule, error) {
	cmd := exec.Command("go", "list", "-e", "-deps", "-test",
		"-json=ImportPath,Dir,Standard,ForTest,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,IgnoredGoFiles,Module,Error", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return judgedModule{}, fmt.Errorf("go list in %s: %w\n%s", dir, err, strings.TrimSpace(errOut.String()))
	}
	mod := judgedModule{built: map[string]bool{}, reached: map[string]listedPackage{}}
	var unmatched, broken []string
	dec := json.NewDecoder(&out)
	for {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return judgedModule{}, fmt.Errorf("reading go list's output in %s: %w", dir, err)
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
		selected := slices.Concat(p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles)
		// go list also reports an error for a package whose every file the build constraints leave out, and the walk judges those files.
		if p.Error != nil && (len(selected) > 0 || len(p.IgnoredGoFiles) == 0) {
			broken = append(broken, p.Error.Err)
		}
		rel, err := filepath.Rel(p.Module.Dir, p.Dir)
		if err != nil {
			return judgedModule{}, err
		}
		for _, f := range selected {
			mod.built[filepath.ToSlash(filepath.Join(rel, f))] = true
		}
	}
	if mod.path == "" {
		if warning := strings.TrimSpace(errOut.String()); warning != "" {
			unmatched = append(unmatched, warning)
		}
		return judgedModule{}, fmt.Errorf("go list found no package of a Go module in %s, so nothing was judged: %s", dir, strings.Join(unmatched, "; "))
	}
	if len(broken) > 0 {
		return judgedModule{}, fmt.Errorf("go list cannot load every package of %s, so its imports were not judged: %s", mod.path, strings.Join(broken, "; "))
	}
	files, err := moduleFiles(mod.dir)
	if err != nil {
		return judgedModule{}, fmt.Errorf("walking %s: %w", mod.dir, err)
	}
	for f := range mod.built {
		if !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	mod.files = files
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
	// unjudged counts the imports of left-out files that go list loaded no package for, which only the core module check judged.
	unjudged int
}

// judge names each file by its path under prefix, the module's directory relative to the tree the run judges.
func judge(mod judgedModule, prefix string) (judgement, error) {
	var j judgement
	types := map[string]string{}
	fset := token.NewFileSet()
	for _, file := range mod.files {
		parsed, err := parser.ParseFile(fset, filepath.Join(mod.dir, filepath.FromSlash(file)), nil, parser.ImportsOnly)
		if err != nil {
			return judgement{}, fmt.Errorf("parsing %s: %w", file, err)
		}
		name := filepath.ToSlash(filepath.Join(prefix, file))
		j.files++
		for _, spec := range parsed.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return judgement{}, fmt.Errorf("%s: reading import %s: %w", name, spec.Path.Value, err)
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
			if !ok && !mod.built[file] {
				// The standard library's paths are the ones whose first element holds no dot.
				if first, _, _ := strings.Cut(imp, "/"); strings.Contains(first, ".") {
					j.unjudged++
				}
				continue
			}
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

// manifestType reads only an unindented type: line, since an indented one belongs to another key, and gives "" for a module with no manifest.yaml.
func manifestType(dir string) (string, error) {
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("reading the module at %s: %w", dir, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	text := strings.TrimPrefix(string(raw), "\ufeff")
	for _, line := range strings.Split(text, "\n") {
		value, ok := strings.CutPrefix(line, "type:")
		if !ok {
			continue
		}
		// YAML starts a comment at a # that follows a space or a tab.
		for i := 1; i < len(value); i++ {
			if value[i] == '#' && (value[i-1] == ' ' || value[i-1] == '\t') {
				value = value[:i]
				break
			}
		}
		return strings.Trim(strings.TrimSpace(value), `"'`), nil
	}
	return "", nil
}
