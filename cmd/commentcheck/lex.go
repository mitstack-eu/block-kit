package main

import (
	"path"
	"strings"
)

type span struct{ start, end int }

type tsToken struct {
	text string
	nl   bool
}

type lexer struct {
	src      string
	ts, jsx  bool
	i        int
	regexOK  bool
	nl       bool
	comments []span
	tokens   []tsToken
}

const regexAfter = "(,=:[!&|?{;+-*/%<>~^"

var regexKeywords = map[string]bool{
	"return": true, "typeof": true, "case": true, "do": true, "else": true, "in": true, "of": true,
	"new": true, "delete": true, "void": true, "throw": true, "yield": true, "await": true,
}

var punctuators = []string{
	">>>=", "...", "===", "!==", "**=", "<<=", ">>=", ">>>", "&&=", "||=", "??=",
	"=>", "==", "!=", "<=", ">=", "&&", "||", "??", "?.", "++", "--",
	"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "**", "<<", ">>",
}

func lexFile(p, src string) *lexer {
	l := &lexer{src: src, ts: !isGo(p), jsx: path.Ext(p) == ".tsx", regexOK: true}
	l.code(false)
	return l
}

func (l *lexer) emit(start int, regexOK bool) {
	l.tokens = append(l.tokens, tsToken{l.src[start:l.i], l.nl && len(l.tokens) > 0})
	l.nl, l.regexOK = false, regexOK
}

func (l *lexer) last() string {
	if len(l.tokens) == 0 {
		return ""
	}
	return l.tokens[len(l.tokens)-1].text
}

func (l *lexer) blank() {
	for l.i < len(l.src) {
		start, rest := l.i, l.src[l.i:]
		switch {
		case rest[0] == '\n':
			l.nl = true
			l.i++
		case isSpace(rest[0]):
			l.i++
		case strings.HasPrefix(rest, "//"):
			l.i = lineEnd(l.src, start)
			l.comments = append(l.comments, span{start, l.i})
		case strings.HasPrefix(rest, "/*"):
			l.i = len(l.src)
			if end := strings.Index(rest[2:], "*/"); end >= 0 {
				l.i = start + end + 4
			}
			l.nl = l.nl || strings.Contains(l.src[start:l.i], "\n")
			l.comments = append(l.comments, span{start, l.i})
		default:
			return
		}
	}
}

func (l *lexer) code(nested bool) {
	depth := 0
	for l.blank(); l.i < len(l.src); l.blank() {
		start, c := l.i, l.src[l.i]
		switch {
		case c == '"' || c == '\'':
			l.quoted(c, true)
			l.emit(start, false)
		case c == '`' && l.ts:
			l.template(start)
		case c == '`':
			l.quoted(c, false)
			l.emit(start, false)
		case c == '/' && l.ts && l.regexStarts():
			l.regex()
			l.emit(start, false)
		case c == '<' && l.jsx && l.regexOK && !l.typeParams():
			l.element()
		case isWord(c):
			for l.i < len(l.src) && isWord(l.src[l.i]) {
				l.i++
			}
			l.emit(start, regexKeywords[l.src[start:l.i]] && l.last() != "." && l.last() != "?.")
		case c == '}' && nested && depth == 0:
			return
		default:
			l.i += punctuatorLen(l.src[start:])
			p := l.src[start:l.i]
			depth += strings.Count(p, "{") - strings.Count(p, "}")
			l.emit(start, p != "++" && p != "--" && strings.IndexByte(regexAfter, p[len(p)-1]) >= 0)
		}
	}
}

func (l *lexer) regexStarts() bool {
	if l.i > 0 && l.src[l.i-1] == '<' {
		return false
	}
	if l.regexOK {
		return true
	}
	lineStart := strings.TrimRight(l.src[:l.i], " \t")
	return l.nl && l.last() == "}" && strings.HasSuffix(lineStart, "\n")
}

func (l *lexer) typeParams() bool {
	rest := strings.TrimLeft(l.src[l.i+1:], " \t\r\n")
	name := 0
	for name < len(rest) && isWord(rest[name]) {
		name++
	}
	if name == 0 {
		return false
	}
	rest = strings.TrimLeft(rest[name:], " \t\r\n")
	return strings.HasPrefix(rest, ",") || strings.HasPrefix(rest, "extends ")
}

func (l *lexer) element() {
	for depth := 0; l.i < len(l.src); {
		closing, selfClosing := l.tag()
		switch {
		case closing:
			depth--
		case !selfClosing:
			depth++
		}
		if depth <= 0 {
			break
		}
		l.children()
	}
	l.regexOK = false
}

func (l *lexer) tag() (closing, selfClosing bool) {
	start := l.i
	l.i++
	l.emit(start, false)
	for l.blank(); l.i < len(l.src); l.blank() {
		start, c := l.i, l.src[l.i]
		switch {
		case c == '>':
			l.i++
			l.emit(start, false)
			return closing, false
		case c == '/' && l.last() == "<":
			closing = true
			l.i++
			l.emit(start, false)
		case strings.HasPrefix(l.src[start:], "/>"):
			l.i += 2
			l.emit(start, false)
			return closing, true
		case c == '"' || c == '\'':
			l.quoted(c, false)
			l.emit(start, false)
		case c == '{':
			l.braced()
		case isWord(c):
			for l.i < len(l.src) && isWord(l.src[l.i]) {
				l.i++
			}
			l.emit(start, false)
		default:
			l.i++
			l.emit(start, false)
		}
	}
	return closing, false
}

func (l *lexer) children() {
	for l.i < len(l.src) && l.src[l.i] != '<' {
		if l.src[l.i] == '{' {
			l.braced()
			continue
		}
		start := l.i
		for l.i < len(l.src) && l.src[l.i] != '<' && l.src[l.i] != '{' {
			l.i++
		}
		if strings.TrimSpace(l.src[start:l.i]) != "" {
			l.emit(start, false)
		}
	}
}

func (l *lexer) braced() {
	start := l.i
	l.i++
	l.emit(start, true)
	l.code(true)
	if l.i < len(l.src) {
		start = l.i
		l.i++
		l.emit(start, false)
	}
}

func (l *lexer) quoted(q byte, escapes bool) {
	for l.i++; l.i < len(l.src); l.i++ {
		switch c := l.src[l.i]; {
		case escapes && c == '\\':
			l.i++
		case c == q:
			l.i++
			return
		case c == '\n' && q != '`':
			return
		}
	}
	l.i = min(l.i, len(l.src))
}

func (l *lexer) template(start int) {
	for l.i++; l.i < len(l.src); {
		switch {
		case l.src[l.i] == '\\':
			l.i += 2
		case l.src[l.i] == '`':
			l.i++
			l.emit(start, false)
			return
		case strings.HasPrefix(l.src[l.i:], "${"):
			l.i += 2
			l.emit(start, true)
			l.code(true)
			start = l.i
			l.i = min(l.i+1, len(l.src))
		default:
			l.i++
		}
	}
	l.i = min(l.i, len(l.src))
	if start < l.i {
		l.emit(start, false)
	}
}

func (l *lexer) regex() {
	inClass := false
	for l.i++; l.i < len(l.src); l.i++ {
		switch c := l.src[l.i]; {
		case c == '\\':
			l.i++
		case c == '\n':
			return
		case inClass:
			inClass = c != ']'
		case c == '[':
			inClass = true
		case c == '/':
			l.i++
			return
		}
	}
	l.i = min(l.i, len(l.src))
}

func punctuatorLen(s string) int {
	for _, p := range punctuators {
		if strings.HasPrefix(s, p) {
			return len(p)
		}
	}
	return 1
}

func lineEnd(src string, i int) int {
	if n := strings.IndexByte(src[i:], '\n'); n >= 0 {
		return i + n
	}
	return len(src)
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v'
}

func isWord(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}
