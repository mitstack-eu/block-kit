package main

import (
	"fmt"
	"io"
	"regexp"
	"slices"
)

const closing = "A comment says what the code does or its constraint: no ticket id, no history. " +
	"Carry the ticket id in the commit subject and the pull-request title, name a function rather than a file:line, " +
	"and delete a comment that restates the code."

var commentRules = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`\b(?:MIT|MS)-\d+\b`), "a ticket id"},
	{regexp.MustCompile(`\b[\w.-]+\.(?:go|ts|tsx|js|jsx|py|sh|sql|ya?ml):\d+\b`), "a file:line reference"},
}

type sides struct {
	base, head map[string]string
}

func (s sides) judged(c change) bool {
	b, hasBase := s.base[c.base]
	h, hasHead := s.head[c.head]
	return hasBase && !generated(c.base, b) || hasHead && !generated(c.head, h)
}

func readSides(g gitRepo, base string, changes []change) (sides, error) {
	changed := map[string]bool{}
	for _, c := range changes {
		changed[c.base], changed[c.head] = true, true
	}
	keep := func(p string) bool { return changed[p] }
	tree, err := g.tree(base)
	if err != nil {
		return sides{}, err
	}
	var s sides
	if s.base, err = g.read(tree, keep); err != nil {
		return sides{}, err
	}
	if s.head, err = g.headFiles(keep); err != nil {
		return sides{}, err
	}
	return s, nil
}

func runCheck(stdout io.Writer, g gitRepo, base, short string, paths []string) (int, error) {
	changes, err := g.changes(base, true, paths)
	if err != nil {
		return 2, err
	}
	s, err := readSides(g, base, changes)
	if err != nil {
		return 2, err
	}

	var findings, shares []string
	n := 0
	for _, c := range changes {
		if !s.judged(c) {
			continue
		}
		n++
		src, ok := s.head[c.head]
		if !ok || generated(c.head, src) {
			continue
		}
		if c.base != "" {
			if c.added, err = g.addedLines(base, c); err != nil {
				return 2, err
			}
		}
		findings = append(findings, commentFindings(c, src)...)
		shares = append(shares, fileShareFinding(c.head, src)...)
	}
	slices.Sort(shares)
	findings = append(findings, shares...)

	if len(findings) == 0 {
		fmt.Fprintf(stdout, "commentcheck: %d files judged against %s, no finding\n", n, short)
		return 0, nil
	}
	for _, f := range findings {
		fmt.Fprintln(stdout, f)
	}
	fmt.Fprintf(stdout, "\n%s\n", closing)
	return 1, nil
}

func commentFindings(c change, src string) []string {
	var out []string
	k, comments := 0, lexFile(c.head, src).comments
	for line, start := 1, 0; start < len(src); line, start = line+1, lineEnd(src, start)+1 {
		end := lineEnd(src, start)
		for k < len(comments) && comments[k].end <= start {
			k++
		}
		if c.added != nil && !c.added[line] {
			continue
		}
		for j := k; j < len(comments) && comments[j].start < end; j++ {
			text := src[max(comments[j].start, start):min(comments[j].end, end)]
			for _, r := range commentRules {
				for _, m := range r.re.FindAllString(text, -1) {
					out = append(out, fmt.Sprintf("%s:%d: a comment holds %s (%s)", c.head, line, r.what, m))
				}
			}
		}
	}
	return out
}

func fileShareFinding(p, src string) []string {
	kind, target := "production", productionTarget
	if isTest(p) {
		kind, target = "test", testTarget
	}
	s := fileShare(p, src)
	if !s.above(target) {
		return nil
	}
	return []string{fmt.Sprintf("%s: %s comment share %s (%d/%d), above the %d%% target",
		p, kind, s.percent(), s.comments, s.lines, target)}
}
