// Package kitbinding, part of mitstack's own tooling, holds the one definition of a block importing the kit's handler, the rule blockverify judges every block by.
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
const HandlerPackage = "github.com/mitstack-eu/block-kit/handler"

// UnservableBlock opens the error kittest reports when the kit's handler refuses a block's Config, so blockverify can quote it.
const UnservableBlock = "the block cannot be served: "

// DrillRanPrefix opens the line kittest logs for each unit it drilled, so blockverify can read which units a block's replacement drill covered.
const DrillRanPrefix = "kittest.AssertReplacement drill of "

// DrillRan is the line kittest logs once it drilled unit through stages.
func DrillRan(unit string, stages []string) string {
	return DrillRanPrefix + strconv.Quote(unit) + " ran: " + strings.Join(stages, ", ")
}

// DrilledUnits reads from a test run's output each unit a drill logged, with the stages it ran.
func DrilledUnits(output string) map[string]string {
	drilled := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		_, rest, found := strings.Cut(line, DrillRanPrefix)
		if !found {
			continue
		}
		quoted, err := strconv.QuotedPrefix(rest)
		if err != nil {
			continue
		}
		unit, err := strconv.Unquote(quoted)
		stages, ran := strings.CutPrefix(rest[len(quoted):], " ran: ")
		if err != nil || !ran {
			continue
		}
		drilled[unit] = strings.TrimSpace(stages)
	}
	return drilled
}

// HandlerImportFiles returns the sorted, slash-separated paths under dir of non-test files a default build compiles that import HandlerPackage, and fails the scan on any file it cannot judge.
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

func skippedDir(name string) bool {
	return name == "testdata" || name == "vendor" ||
		strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".")
}
