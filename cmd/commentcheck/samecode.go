package main

import (
	"cmp"
	"fmt"
	"go/scanner"
	"go/token"
	"io"
	"slices"
	"strings"
)

var directivePrefixes = []string{"//go:", "//line ", "/*line ", "// +build", "//export ", "//extern "}

type goCode struct {
	tokens, directives, comments []string
	cgo, ok                      bool
}

func runSameCode(stdout io.Writer, g gitRepo, base, short string, paths []string) (int, error) {
	changes, err := g.changes(base, false, paths)
	if err != nil {
		return 2, err
	}
	s, err := readSides(g, base, changes)
	if err != nil {
		return 2, err
	}
	n, code := 0, 0
	for _, c := range changes {
		if !s.judged(c) {
			continue
		}
		n++
		p := cmp.Or(c.head, c.base)
		b, hasBase := s.base[c.base]
		h, hasHead := s.head[c.head]
		if !sameCode(p, b, hasBase, h, hasHead) {
			fmt.Fprintf(stdout, "%s: code differs from %s\n", p, short)
			code = 1
		}
	}
	if code == 0 {
		fmt.Fprintf(stdout, "commentcheck: %d Go and TypeScript files carry the same code as %s\n", n, short)
	}
	return code, nil
}

func sameCode(p, base string, hasBase bool, head string, hasHead bool) bool {
	if !isGo(p) {
		return slices.Equal(tsCode(p, base, hasBase), tsCode(p, head, hasHead))
	}
	b, h := scanGo(p, base), scanGo(p, head)
	switch {
	case !hasBase:
		return h.docOnly()
	case !hasHead:
		return b.docOnly()
	case !b.ok || !h.ok || !slices.Equal(b.tokens, h.tokens):
		return false
	case b.cgo || h.cgo:
		return slices.Equal(b.comments, h.comments)
	}
	return slices.Equal(b.directives, h.directives)
}

func tsCode(p, src string, present bool) []tsToken {
	if !present {
		return nil
	}
	return lexFile(p, src).tokens
}

func isOutputMarker(comment string) bool {
	return strings.HasPrefix(comment, "// Output:") || strings.HasPrefix(comment, "// Unordered output:")
}

func scanGo(p, src string) goCode {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)
	var c goCode
	var group []string
	groupEnd := 0
	flush := func() {
		if i := slices.IndexFunc(group, isOutputMarker); i >= 0 && isTest(p) {
			c.directives = append(c.directives, group[i:]...)
		}
		group = nil
	}
	importing, inParens := false, false
	for {
		pos, tok, lit := s.Scan()
		if tok == token.COMMENT {
			if file.Line(pos) > groupEnd+1 {
				flush()
			}
			group, groupEnd = append(group, lit), file.Line(pos)+strings.Count(lit, "\n")
			c.comments = append(c.comments, lit)
			if slices.ContainsFunc(directivePrefixes, func(p string) bool { return strings.HasPrefix(lit, p) }) {
				c.directives = append(c.directives, lit)
			}
			continue
		}
		flush()
		switch {
		case tok == token.EOF:
			c.ok = s.ErrorCount == 0
			return c
		case tok == token.IMPORT:
			importing = true
		case importing && tok == token.LPAREN:
			inParens = true
		case importing && tok == token.STRING && lit == `"C"`:
			c.cgo = true
		case importing && (tok == token.RPAREN || tok == token.SEMICOLON && !inParens):
			importing, inParens = false, false
		}
		key := tok.String()
		if tok != token.SEMICOLON {
			key += " " + lit
		}
		c.tokens = append(c.tokens, key)
	}
}

func (c goCode) docOnly() bool {
	return c.ok && len(c.tokens) == 3 && c.tokens[0] == "package package" &&
		strings.HasPrefix(c.tokens[1], "IDENT ") && c.tokens[2] == ";" && len(c.directives) == 0
}
