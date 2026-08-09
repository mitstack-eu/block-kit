package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// concatGoTestFiles recursively concatenates every *_test.go file under dir,
// so a presence check (grep-equivalent) works regardless of how a target
// lays out its tree — mirrors the recursive `grep -rq ... --include='*_test.go'`
// scripts/check-operator-contract-tests.sh uses.
func concatGoTestFiles(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr == nil {
			b.Write(raw)
			b.WriteByte('\n')
		}
		return nil
	})
	return b.String()
}

// anyGoFileContains reports whether any non-test .go file under dir contains
// needle — mirrors sibling scripts' `grep -rqsl '<needle>' "$dir"/*.go`.
func anyGoFileContains(dir, needle string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr == nil && strings.Contains(string(raw), needle) {
			found = true
		}
		return nil
	})
	return found
}
