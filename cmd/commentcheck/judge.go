package main

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

const (
	productionTarget = 12
	testTarget       = 8
)

var (
	generatedRE = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)
	tsTestRE    = regexp.MustCompile(`\.(test|spec)\.tsx?$`)
	skippedDirs = []string{"testdata", "node_modules", "vendor"}
)

func judgedPath(p string) bool {
	switch path.Ext(p) {
	case ".go", ".ts", ".tsx":
	default:
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if slices.Contains(skippedDirs, seg) {
			return false
		}
	}
	return true
}

func isGo(p string) bool { return strings.HasSuffix(p, ".go") }

func isTest(p string) bool {
	if isGo(p) {
		return strings.HasSuffix(p, "_test.go")
	}
	return tsTestRE.MatchString(path.Base(p)) || slices.Contains(strings.Split(path.Dir(p), "/"), "__tests__")
}

func generated(p, src string) bool {
	return isGo(p) && generatedRE.MatchString(src)
}

type share struct{ comments, lines int }

type tally struct{ prod, test share }

func (t *tally) add(p, src string) {
	if generated(p, src) {
		return
	}
	s := &t.prod
	if isTest(p) {
		s = &t.test
	}
	c, n := measure(src, lexFile(p, src).comments)
	s.comments += c
	s.lines += n
}

func measure(src string, comments []span) (commentLines, total int) {
	k := 0
	for start := 0; start < len(src); start = lineEnd(src, start) + 1 {
		total++
		first, end := start, lineEnd(src, start)
		for first < end && isSpace(src[first]) {
			first++
		}
		for k < len(comments) && comments[k].end <= first {
			k++
		}
		if first < end && k < len(comments) && comments[k].start <= first {
			commentLines++
		}
	}
	return commentLines, total
}

func (s share) percent() string {
	if s.lines == 0 {
		return "0.0%"
	}
	permille := (2000*s.comments + s.lines) / (2 * s.lines)
	return fmt.Sprintf("%d.%d%%", permille/10, permille%10)
}

func (s share) above(target int) bool { return s.comments*100 > target*s.lines }

func (s share) exceeds(base share) bool {
	return base.lines == 0 || s.comments*base.lines > base.comments*s.lines
}

func rose(base, head share, target int) bool {
	return head.lines > 0 && head.above(target) && head.exceeds(base)
}
