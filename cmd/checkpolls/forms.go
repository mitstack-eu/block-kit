package main

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

type form struct {
	label    string
	judges   func(file string) bool
	re       *regexp.Regexp
	comments []string
}

// Shell hides in many file types, a workflow, a Go string or a template among them, so the sleep form judges every file but prose, which describes a wait rather than running one.
var forms = []form{
	{"time.Sleep", goSource, regexp.MustCompile(`\btime\.Sleep\(`), []string{"//"}},
	{"time.Tick", goSource, regexp.MustCompile(`\btime\.Tick\(`), []string{"//"}},
	{"time.NewTicker", goSource, regexp.MustCompile(`\btime\.NewTicker\(`), []string{"//"}},
	{"setInterval", scriptSource, regexp.MustCompile(`\bsetInterval\s*\(`), []string{"//"}},
	{"refetchInterval", scriptSource, regexp.MustCompile(`\brefetchInterval\b`), []string{"//"}},
	{"a shell sleep", notProse, regexp.MustCompile(`(?:^|[;&|({"']|\b(?:do|then|else|run:))\s*sleep\s+["']?(?:\$|\.?\d)`), []string{"#", "//"}},
}

func goSource(file string) bool {
	return strings.HasSuffix(file, ".go") || strings.HasSuffix(file, ".go.tmpl")
}

func scriptSource(file string) bool {
	return slices.Contains([]string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}, path.Ext(file))
}

func notProse(file string) bool {
	return !slices.Contains([]string{".md", ".mdx", ".markdown", ".txt", ".rst", ".adoc"}, path.Ext(file))
}

// match skips a line that opens with its form's comment marker, since a commented-out line runs nothing.
func match(l addedLine) (form, bool) {
	text := strings.TrimSpace(l.text)
	for _, f := range forms {
		commented := slices.ContainsFunc(f.comments, func(c string) bool { return strings.HasPrefix(text, c) })
		if f.judges(l.path) && !commented && f.re.MatchString(l.text) {
			return f, true
		}
	}
	return form{}, false
}
