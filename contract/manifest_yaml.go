package contract

import (
	"errors"
	"fmt"
	"strings"
)

type yamlKind int

const (
	yamlNull yamlKind = iota
	yamlScalar
	yamlMap
	yamlSeq
	// yamlOpaque is a value the reader cannot take: an alias, a value YAML refuses unquoted, or a collection or quoted scalar it cannot close.
	yamlOpaque
)

type yamlNode struct {
	kind  yamlKind
	value string
	keys  []string
	vals  []*yamlNode
	items []*yamlNode
	// str marks a scalar YAML reads as a string whatever its text.
	str bool
	// misplaced marks a mapping holding a line that is neither a key of it nor inside one of its values.
	misplaced bool
	alias     bool
	// reserved is the character, @, ` or %, that YAML refuses at the start of an unquoted scalar.
	reserved byte
	// tag is a tag YAML or the reader refuses on the value, and tagNote says why.
	tag, tagNote string
}

// get gives the first value of a key written twice.
func (n *yamlNode) get(key string) *yamlNode {
	if n == nil || n.kind != yamlMap {
		return nil
	}
	for i, k := range n.keys {
		if k == key {
			return n.vals[i]
		}
	}
	return nil
}

// parseYAML reads one YAML document; anything it cannot read becomes an opaque node the manifest decoder refuses.
//
// A %YAML 1.1 directive, a start marker, content on its line and an end marker are read; any other directive, a second document or a line outside the top-level value is an error.
func parseYAML(content string) (*yamlNode, error) {
	r := &yamlReader{lines: strings.Split(strings.TrimPrefix(content, "\ufeff"), "\n")}
	root := r.nested(-1, false)
	_, _, outside := r.peek()
	switch {
	case r.err != nil:
		return nil, r.err
	case outside:
		return nil, fmt.Errorf("line %d is outside the document's top-level value", r.pos+1)
	}
	return root, nil
}

var errSecondDocument = errors.New("the manifest holds more than one YAML document, and a manifest is one document, so write it without a second ---")

type yamlReader struct {
	lines     []string
	pos       int
	started   bool
	opened    bool
	directive bool
	ended     bool
	// err stops reading: a second document or a directive the reader does not take.
	err error
}

func atDocumentMarker(s string) bool {
	for _, marker := range []string{"---", "..."} {
		if rest, ok := strings.CutPrefix(s, marker); ok && (rest == "" || strings.IndexByte(" \t\n", rest[0]) >= 0) {
			return true
		}
	}
	return false
}

// peek returns the next line holding content, skipping blank and comment lines, a %YAML 1.1 directive and document markers.
//
// A second document or an unsupported directive sets r.err, and peek then reports no further line.
func (r *yamlReader) peek() (indent int, text string, ok bool) {
	for ; r.err == nil && r.pos < len(r.lines); r.pos++ {
		line := strings.TrimSuffix(r.lines[r.pos], "\r")
		text := strings.TrimLeft(line, " \t")
		indent := len(line) - len(text)
		if text == "" || text[0] == '#' {
			continue
		}
		switch {
		case indent == 0 && strings.HasPrefix(line, "...") && atDocumentMarker(line):
			r.ended = true
			continue
		case indent == 0 && atDocumentMarker(line):
			if r.started || r.ended || r.opened {
				r.err = errSecondDocument
				continue
			}
			r.opened = true
			if rest, err := stripYAMLComment(line[3:]); err == nil && strings.TrimSpace(rest) == "" {
				continue
			}
			r.lines[r.pos] = "   " + line[3:]
			text = strings.TrimLeft(line[3:], " \t")
			indent = len(line) - len(text)
		case indent == 0 && text[0] == '%' && !r.started && !r.opened:
			if directive, err := stripYAMLComment(text); err != nil || strings.Join(strings.Fields(directive), " ") != "%YAML 1.1" {
				r.err = fmt.Errorf("%s is a directive the kit's YAML reader does not take, and a manifest needs none", strings.TrimSpace(text))
			}
			r.directive = true
			continue
		}
		switch {
		case r.ended:
			r.err = errSecondDocument
			continue
		case r.directive && !r.opened:
			r.err = errors.New("a %YAML directive must be followed by a document start marker, ---")
			continue
		}
		r.started = true
		return indent, text, true
	}
	return 0, "", false
}

func (r *yamlReader) skipDeeper(owner int) {
	for {
		indent, _, ok := r.peek()
		if !ok || indent <= owner {
			return
		}
		r.pos++
	}
}

// nested reads the node after a key or item at indent parent; compact admits a sequence at the parent's own indent, which YAML allows under a mapping key.
func (r *yamlReader) nested(parent int, compact bool) *yamlNode {
	indent, text, ok := r.peek()
	if !ok || indent < parent || indent == parent && !(compact && isSeqItem(text)) {
		return &yamlNode{kind: yamlNull}
	}
	if isSeqItem(text) {
		return r.sequence(indent)
	}
	if _, _, isKey := splitKey(text); isKey {
		return r.mapping(indent)
	}
	r.pos++
	return r.value(text, parent, false)
}

func (r *yamlReader) mapping(indent int) *yamlNode {
	n := &yamlNode{kind: yamlMap}
	for {
		ind, text, ok := r.peek()
		if !ok || ind < indent {
			return n
		}
		key, rest, isKey := splitKey(text)
		if ind > indent || !isKey {
			n.misplaced = true
			r.pos++
			r.skipDeeper(ind)
			continue
		}
		r.pos++
		n.keys = append(n.keys, key)
		n.vals = append(n.vals, r.value(rest, indent, true))
	}
}

func (r *yamlReader) sequence(indent int) *yamlNode {
	n := &yamlNode{kind: yamlSeq}
	for {
		ind, text, ok := r.peek()
		if !ok || ind != indent || !isSeqItem(text) {
			return n
		}
		content := strings.TrimLeft(text[1:], " \t")
		if _, _, isKey := splitKey(content); isKey || isSeqItem(content) {
			// The item's content is a node at its own column, as if it began on the next line.
			r.lines[r.pos] = strings.Repeat(" ", ind+len(text)-len(content)) + content
			n.items = append(n.items, r.nested(ind, false))
			continue
		}
		r.pos++
		n.items = append(n.items, r.value(content, ind, false))
	}
}

func (r *yamlReader) value(rest string, owner int, compact bool) *yamlNode {
	if stripped, err := stripYAMLComment(rest); err == nil {
		rest = stripped
	}
	rest, tag := splitProperties(strings.TrimSpace(rest))
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return withTag(r.nested(owner, compact), tag)
	}
	return withTag(r.inline(rest, owner), tag)
}

func (r *yamlReader) inline(text string, owner int) *yamlNode {
	switch text[0] {
	case '|', '>':
		return r.blockScalar(text, owner)
	case '*':
		r.skipDeeper(owner)
		return &yamlNode{kind: yamlOpaque, alias: true}
	case '@', '`', '%':
		// YAML reserves these at the start of a plain scalar.
		r.skipDeeper(owner)
		return &yamlNode{kind: yamlOpaque, reserved: text[0]}
	case '[', '{':
		return r.flowCollection(text, owner)
	case '"', '\'':
		return r.quotedScalar(text, owner)
	}
	if isSeqItem(text) || text == "?" || strings.HasPrefix(text, "? ") || strings.HasPrefix(text, "?\t") {
		r.skipDeeper(owner)
		return &yamlNode{kind: yamlOpaque}
	}
	return r.plainScalar(text, owner)
}

// quotedScalar runs over the following lines up to the closing quote whatever their indent.
//
// A scalar left open, text other than a comment after the closing quote, or a deeper line after it is opaque.
func (r *yamlReader) quotedScalar(text string, owner int) *yamlNode {
	end := closingQuote(text)
	if end < 0 && r.pos > 0 {
		// Escaped blanks at the end of the first line are part of the scalar.
		raw := strings.TrimSuffix(r.lines[r.pos-1], "\r")
		if trimmed := strings.TrimRight(raw, " \t"); strings.HasSuffix(trimmed, text) {
			text = raw[len(trimmed)-len(text):]
		}
	}
	lines := []string{text}
	for end < 0 && r.pos < len(r.lines) {
		line := strings.TrimSuffix(r.lines[r.pos], "\r")
		if atDocumentMarker(line) {
			break
		}
		r.pos++
		lines = append(lines, line)
		end = closingQuote(strings.Join(lines, "\n"))
	}
	joined := strings.Join(lines, "\n")
	opaque := &yamlNode{kind: yamlOpaque}
	if end < 0 {
		return opaque
	}
	if after, err := stripYAMLComment(joined[end+1:]); err != nil || strings.TrimSpace(after) != "" {
		return opaque
	}
	if ind, _, ok := r.peek(); ok && ind > owner {
		r.skipDeeper(owner)
		return opaque
	}
	v, scanned, ok := scanQuoted(joined)
	if !ok || scanned != end+1 {
		return opaque
	}
	return &yamlNode{kind: yamlScalar, value: v, str: true}
}

func closingQuote(s string) int {
	quote := s[0]
	for i := 1; i < len(s); i++ {
		i = skipQuotedYAML(s, i, &quote)
		if quote == 0 {
			return i
		}
	}
	return -1
}

// plainScalar joins continuation lines with a blank as YAML folds them; a line holding a key or a mapping-value colon makes the value opaque.
func (r *yamlReader) plainScalar(text string, owner int) *yamlNode {
	parts := []string{text}
	opaque := holdsMappingValue(text)
	for {
		ind, next, ok := r.peek()
		if !ok || ind <= owner {
			break
		}
		r.pos++
		if _, _, isKey := splitKey(next); isKey {
			opaque = true
		}
		if stripped, err := stripYAMLComment(next); err == nil {
			next = stripped
		}
		next = strings.TrimSpace(next)
		opaque = opaque || holdsMappingValue(next)
		parts = append(parts, next)
	}
	if opaque {
		return &yamlNode{kind: yamlOpaque}
	}
	return &yamlNode{kind: yamlScalar, value: strings.Join(parts, " ")}
}

func holdsMappingValue(s string) bool {
	return strings.Contains(s, ": ") || strings.Contains(s, ":\t") || strings.HasSuffix(s, ":")
}

// blockScalar keeps the final line break once, drops it under "-" and keeps every trailing blank line under "+".
func (r *yamlReader) blockScalar(header string, owner int) *yamlNode {
	literal := header[0] == '|'
	chomp := byte(0)
	indent := -1
	for _, c := range []byte(header[1:]) {
		switch {
		case c == '-' || c == '+':
			chomp = c
		case c >= '1' && c <= '9':
			indent = owner + int(c-'0')
			if owner < 0 {
				indent = int(c - '0')
			}
		}
	}

	var body []string
	for ; r.pos < len(r.lines); r.pos++ {
		line := strings.TrimSuffix(r.lines[r.pos], "\r")
		text := strings.TrimLeft(line, " \t")
		ind := len(line) - len(text)
		if text != "" && ind <= owner {
			break
		}
		if text != "" && indent < 0 {
			indent = ind
		}
		if text == "" || indent < 0 || ind < indent {
			body = append(body, text)
			continue
		}
		body = append(body, line[indent:])
	}

	trailing := 0
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
		trailing++
	}
	var value string
	if literal {
		value = strings.Join(body, "\n")
	} else {
		value = foldLines(body)
	}
	if len(body) > 0 {
		switch chomp {
		case 0:
			value += "\n"
		case '+':
			value += strings.Repeat("\n", trailing+1)
		}
	}
	return &yamlNode{kind: yamlScalar, value: value, str: true}
}

// foldLines turns a break between two text lines into a blank, a run of blank lines into that many breaks, and keeps the breaks around a more indented line.
func foldLines(lines []string) string {
	var b strings.Builder
	blanks := 0
	prev := ""
	for i, line := range lines {
		if line == "" {
			blanks++
			continue
		}
		if i > 0 {
			moreIndented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") ||
				strings.HasPrefix(prev, " ") || strings.HasPrefix(prev, "\t")
			switch {
			case blanks == 0 && !moreIndented:
				b.WriteString(" ")
			case moreIndented:
				b.WriteString(strings.Repeat("\n", blanks+1))
			default:
				b.WriteString(strings.Repeat("\n", blanks))
			}
		}
		b.WriteString(line)
		blanks = 0
		prev = line
	}
	return b.String()
}

// joinContinuation's stripComments drops each joined line's comment, which a flow collection allows and a quoted scalar does not hold.
func (r *yamlReader) joinContinuation(text string, owner int, stripComments bool, done func(string) bool) string {
	for !done(text) {
		ind, next, ok := r.peek()
		if !ok || ind <= owner {
			break
		}
		r.pos++
		if stripComments {
			if stripped, err := stripYAMLComment(next); err == nil {
				next = stripped
			}
		}
		text += " " + next
	}
	return text
}

// flowCollection makes one the reader cannot close, or text after its closing bracket, opaque.
func (r *yamlReader) flowCollection(text string, owner int) *yamlNode {
	text = r.joinContinuation(text, owner, true, func(s string) bool { return flowClose(s) >= 0 })
	r.skipDeeper(owner)
	end := flowClose(text)
	if end < 0 || strings.TrimSpace(text[end+1:]) != "" {
		return &yamlNode{kind: yamlOpaque}
	}
	p := &flowParser{s: text[:end+1]}
	n := p.node()
	if p.failed || strings.TrimSpace(p.s[p.i:]) != "" {
		return &yamlNode{kind: yamlOpaque}
	}
	return n
}

// flowClose expects text with comments already removed.
func flowClose(text string) int {
	var quote byte
	depth := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			i = skipQuotedYAML(text, i, &quote)
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// flowParser's failed marks text it cannot read, which makes the whole collection opaque.
type flowParser struct {
	s      string
	i      int
	failed bool
}

func (p *flowParser) skipBlanks() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *flowParser) node() *yamlNode {
	tag := ""
	p.skipBlanks()
	for p.i < len(p.s) && (p.s[p.i] == '&' || p.s[p.i] == '!') {
		// An anchor ends at a flow indicator; a tag, as yaml.v3 scans it, swallows a comma or bracket written straight after it.
		end := " \t,[]{}"
		if p.s[p.i] == '!' {
			end = " \t{}"
		}
		start := p.i
		for p.i < len(p.s) && strings.IndexByte(end, p.s[p.i]) < 0 {
			p.i++
		}
		if p.s[start] == '!' {
			tag = p.s[start:p.i]
		}
		p.skipBlanks()
	}
	return withTag(p.untagged(), tag)
}

func (p *flowParser) untagged() *yamlNode {
	if p.i >= len(p.s) {
		return &yamlNode{kind: yamlNull}
	}
	switch c := p.s[p.i]; c {
	case '[':
		return p.sequence()
	case '{':
		return p.mapping()
	case '*':
		p.plain(false)
		return &yamlNode{kind: yamlOpaque, alias: true}
	case '@', '`':
		p.plain(false)
		return &yamlNode{kind: yamlOpaque, reserved: c}
	case '"', '\'':
		return p.quoted()
	case ',', ']', '}':
		return &yamlNode{kind: yamlNull}
	}
	v := p.plain(false)
	if strings.Contains(v, ": ") {
		// A single-pair mapping inside a flow sequence, which the reader
		// does not take.
		p.failed = true
	}
	return &yamlNode{kind: yamlScalar, value: v}
}

// plain's asKey also ends the scalar at a colon followed by a blank or an indicator.
func (p *flowParser) plain(asKey bool) string {
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if strings.IndexByte(",[]{}", c) >= 0 {
			break
		}
		if asKey && c == ':' && (p.i+1 == len(p.s) || strings.IndexByte(" \t,[]{}", p.s[p.i+1]) >= 0) {
			break
		}
		p.i++
	}
	return strings.TrimSpace(p.s[start:p.i])
}

func (p *flowParser) quoted() *yamlNode {
	quote := p.s[p.i]
	start := p.i
	for p.i++; p.i < len(p.s) && quote != 0; p.i++ {
		p.i = skipQuotedYAML(p.s, p.i, &quote)
	}
	v, err := readYAMLScalar(p.s[start:p.i])
	if quote != 0 || err != nil {
		p.failed = true
		return &yamlNode{kind: yamlOpaque}
	}
	return &yamlNode{kind: yamlScalar, value: v, str: true}
}

func (p *flowParser) sequence() *yamlNode {
	n := &yamlNode{kind: yamlSeq}
	p.i++
	for !p.failed {
		p.skipBlanks()
		if p.i >= len(p.s) {
			p.failed = true
			break
		}
		if p.s[p.i] == ']' {
			p.i++
			break
		}
		n.items = append(n.items, p.node())
		if !p.separator(']') {
			p.failed = true
		}
	}
	return n
}

func (p *flowParser) mapping() *yamlNode {
	n := &yamlNode{kind: yamlMap}
	p.i++
	for !p.failed {
		p.skipBlanks()
		if p.i >= len(p.s) {
			p.failed = true
			break
		}
		if p.s[p.i] == '}' {
			p.i++
			break
		}
		var key string
		if c := p.s[p.i]; c == '"' || c == '\'' {
			k := p.quoted()
			key = k.value
		} else {
			key = p.plain(true)
		}
		p.skipBlanks()
		val := &yamlNode{kind: yamlNull}
		if p.i < len(p.s) && p.s[p.i] == ':' {
			p.i++
			val = p.node()
		}
		n.keys = append(n.keys, key)
		n.vals = append(n.vals, val)
		if !p.separator('}') {
			p.failed = true
		}
	}
	return n
}

// separator leaves a closing bracket for the caller.
func (p *flowParser) separator(closing byte) bool {
	p.skipBlanks()
	if p.i < len(p.s) && p.s[p.i] == ',' {
		p.i++
		return true
	}
	return p.i < len(p.s) && p.s[p.i] == closing
}

func isSeqItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ") || strings.HasPrefix(text, "-\t")
}

// splitKey takes a key that is a quoted scalar or a plain one not starting with a YAML indicator, followed by a colon ending the line or preceding a blank.
func splitKey(text string) (key, rest string, ok bool) {
	if text == "" {
		return "", "", false
	}
	switch c := text[0]; {
	case c == '"' || c == '\'':
		quote, end := c, 1
		for ; end < len(text) && quote != 0; end++ {
			end = skipQuotedYAML(text, end, &quote)
		}
		if quote != 0 {
			return "", "", false
		}
		unquoted, err := readYAMLScalar(text[:end])
		if err != nil {
			return "", "", false
		}
		after := strings.TrimLeft(text[end:], " \t")
		if !strings.HasPrefix(after, ":") || len(after) > 1 && after[1] != ' ' && after[1] != '\t' {
			return "", "", false
		}
		return unquoted, after[1:], true
	case strings.IndexByte("-?:,[]{}#&*!|>%@`", c) >= 0:
		return "", "", false
	}
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '#':
			if text[i-1] == ' ' || text[i-1] == '\t' {
				return "", "", false
			}
		case ':':
			if i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\t' {
				return strings.TrimRight(text[:i], " \t"), text[i+1:], true
			}
		}
	}
	return "", "", false
}

// stripYAMLComment cuts at a "#" at the start of s or after a blank, outside a quoted scalar; a quote opens a scalar only at the start of a token.
func stripYAMLComment(s string) (string, error) {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			i = skipQuotedYAML(s, i, &quote)
			continue
		}
		switch c {
		case '#':
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return s[:i], nil
			}
		case '"', '\'':
			if i == 0 || strings.IndexByte(" \t[{,-", s[i-1]) >= 0 {
				quote = c
			}
		}
	}
	if quote != 0 {
		return "", errors.New("unterminated quoted scalar")
	}
	return s, nil
}

// skipQuotedYAML clears *quote when s[i] closes the scalar.
func skipQuotedYAML(s string, i int, quote *byte) int {
	switch {
	case *quote == '"' && s[i] == '\\':
		return i + 1
	case *quote == '\'' && s[i] == '\'' && i+1 < len(s) && s[i+1] == '\'':
		return i + 1
	case s[i] == *quote:
		*quote = 0
	}
	return i
}
