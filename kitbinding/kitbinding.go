// Package kitbinding holds the one definition of "a block imports the kit's
// handler", which is what binds a kit/v1 block to the kit. blockverify and
// the platform's operator gates both judge a block by it, so the two cannot
// disagree about the same source tree.
//
// A file counts when a default build of the block compiles it and its import
// declarations name HandlerPackage. A comment or string literal naming the
// path does not count, nor does a _test.go file, a file a build constraint
// excludes, or a file under testdata/, vendor/, or a directory whose name
// starts with "_" or ".". A scan that could not read or judge a file is an
// error, never an empty answer.
package kitbinding

import (
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// HandlerPackage is the import path of the kit's handler package.
const HandlerPackage = "github.com/mitstack-eu/operator-kit/handler"

// HandlerImportFiles returns every file under dir that imports HandlerPackage
// in code a default build compiles, as sorted, slash-separated paths relative
// to dir. It reads every candidate file, so a file it cannot read, parse or
// match against the build constraints fails the scan even when another file
// already imports the handler. Every error names the offending path.
func HandlerImportFiles(dir string) ([]string, error) {
	fset := token.NewFileSet()
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && skippedDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		match, err := build.Default.MatchFile(filepath.Dir(path), name)
		if err != nil {
			return fmt.Errorf("%s: matching build constraints: %w", path, err)
		}
		if !match {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parsing imports of %s: %w", path, err)
		}
		for _, spec := range f.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err == nil && p == HandlerPackage {
				rel, err := filepath.Rel(dir, path)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				found = append(found, filepath.ToSlash(rel))
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(found)
	return found, nil
}

// skippedDir reports whether the go tool ignores a directory of this name
// when it builds the packages of a module.
func skippedDir(name string) bool {
	return name == "testdata" || name == "vendor" ||
		strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".")
}
