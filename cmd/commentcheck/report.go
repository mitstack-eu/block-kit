package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

func runReport(stdout io.Writer, dir string, paths []string) (int, error) {
	for _, arg := range paths {
		root := filepath.Join(dir, filepath.FromSlash(arg))
		var t tally
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && p != root && (d.Name() == ".git" || slices.Contains(skippedDirs, d.Name())) {
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil || !d.Type().IsRegular() || !judgedPath(filepath.ToSlash(rel)) {
				return err
			}
			src, err := os.ReadFile(p)
			t.add(filepath.ToSlash(rel), string(src))
			return err
		})
		if err != nil {
			return 2, err
		}
		fmt.Fprintf(stdout, "%s: production %s, test %s\n", arg, t.prod.ratio(), t.test.ratio())
	}
	return 0, nil
}

func (s share) ratio() string {
	return fmt.Sprintf("%d/%d (%s)", s.comments, s.lines, s.percent())
}
