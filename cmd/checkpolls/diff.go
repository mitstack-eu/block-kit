package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/mitstack-eu/block-kit/cmd/internal/gitdiff"
)

type addedLine struct {
	path   string
	number int
	text   string
}

// addedLines returns every line the working tree adds over base, with each untracked file's lines among them, so a change is judged before it is committed too.
func addedLines(base string) ([]addedLine, error) {
	top, err := gitdiff.Run(".", "", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	top = strings.TrimSpace(top)
	if _, err := gitdiff.Run(top, "", "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return nil, fmt.Errorf("-base %q is not a commit", base)
	}
	// Renames are followed so a moved file adds none of the lines it carries.
	patch, err := gitdiff.Run(top, "", slices.Concat(gitdiff.DiffFlags,
		[]string{"--no-relative", "--src-prefix=a/", "--dst-prefix=b/", "-M", "-U0", base, "--"})...)
	if err != nil {
		return nil, err
	}
	lines := parsePatch(patch)
	untracked, err := gitdiff.Untracked(top)
	if err != nil {
		return nil, err
	}
	for _, p := range untracked {
		body, ok, err := gitdiff.ReadFile(filepath.Join(top, filepath.FromSlash(p)))
		if err != nil {
			return nil, err
		}
		if !ok || strings.IndexByte(body, 0) >= 0 {
			continue
		}
		for i, text := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
			lines = append(lines, addedLine{p, i + 1, text})
		}
	}
	return lines, nil
}

// parsePatch reads a zero-context patch, counting each hunk's lines from its header so an added line that starts with +++ is never taken for a file header.
func parsePatch(patch string) []addedLine {
	var lines []addedLine
	path, number, removed, added := "", 0, 0, 0
	for _, ln := range strings.Split(patch, "\n") {
		switch {
		case removed > 0 && strings.HasPrefix(ln, "-"):
			removed--
		case added > 0 && strings.HasPrefix(ln, "+"):
			lines = append(lines, addedLine{path, number, ln[1:]})
			number++
			added--
		case removed > 0 || added > 0:
			// git's no-newline marker sits inside a hunk and counts as neither side.
		case strings.HasPrefix(ln, "+++ "):
			path = headPath(ln[len("+++ "):])
		default:
			removed, number, added, _ = gitdiff.Hunk(ln)
		}
	}
	return lines
}

// headPath reads a +++ header's name, which git quotes when it holds a quote, a backslash or a control character, and ends with a tab when it holds a space.
func headPath(s string) string {
	s = strings.TrimSuffix(s, "\t")
	if unquoted, err := strconv.Unquote(s); err == nil {
		s = unquoted
	}
	return strings.TrimPrefix(s, "b/")
}
