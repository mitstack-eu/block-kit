package contract

import (
	"errors"
	"strconv"
	"strings"
)

// yamlKind is the kind of a node parseYAML builds.
type yamlKind int

const (
	yamlNull yamlKind = iota
	yamlScalar
	yamlMap
	yamlSeq
	// yamlOpaque is an alias, a value YAML refuses unquoted, a flow
	// collection the reader cannot close or a quoted scalar it cannot
	// close.
	yamlOpaque
)

// yamlNode is one node of the YAML subset parseYAML reads.
type yamlNode struct {
	kind  yamlKind
	value string
	keys  []string
	vals  []*yamlNode
	items []*yamlNode
	// flow marks a mapping or sequence written in flow style, as in
	// {a: b} or [a, b].
	flow bool
	// str marks a scalar written in quotes or as a block scalar, which YAML
	// reads as a string whatever its text, never as a null, a number or a
	// boolean.
	str bool
	// misplaced marks a mapping holding a line that is neither a key of it
	// nor inside one of its values.
	misplaced bool
	// alias marks an opaque node written as an alias, *name.
	alias bool
	// reserved is the character, @ or `, that starts an unquoted scalar YAML
	// refuses. Such a node is opaque.
	reserved byte
}

// get returns the value under key in a mapping, or nil. A key written twice
// gives its first value.
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

// followableMap reports whether n is a block mapping whose every line the
// reader placed, with no merge key that could bring in keys from elsewhere.
func (n *yamlNode) followableMap() bool {
	return n.kind == yamlMap && !n.flow && !n.misplaced && n.get("<<") == nil
}

func unfollowable(field string) error {
	return errors.New(field + " is in a form the reader cannot follow (a flow mapping, an alias, a merge key or a misplaced line), so what it declares is unknown")
}

// parseYAML reads content by indentation into block mappings, block
// sequences, flow collections and scalars, including block scalars.
// Anything else becomes an opaque node, so a caller refuses it only where it
// reads.
func parseYAML(content string) *yamlNode {
	r := &yamlReader{lines: strings.Split(content, "\n")}
	return r.nested(-1, false)
}

type yamlReader struct {
	lines []string
	pos   int
}

// peek returns the indent and text of the next line holding content,
// skipping over blank lines, comment lines and document markers.
func (r *yamlReader) peek() (indent int, text string, ok bool) {
	for ; r.pos < len(r.lines); r.pos++ {
		line := strings.TrimSuffix(r.lines[r.pos], "\r")
		text := strings.TrimLeft(line, " \t")
		indent := len(line) - len(text)
		if text == "" || text[0] == '#' {
			continue
		}
		if indent == 0 && (text == "---" || strings.HasPrefix(text, "--- ") || text == "..." || text[0] == '%') {
			continue
		}
		return indent, text, true
	}
	return 0, "", false
}

// skipDeeper skips over every line indented deeper than owner: the rest of
// a value the reader has already taken what it needs from.
func (r *yamlReader) skipDeeper(owner int) {
	for {
		indent, _, ok := r.peek()
		if !ok || indent <= owner {
			return
		}
		r.pos++
	}
}

// nested reads the node on the lines after a key or item at indent parent.
// compact admits a sequence at the parent's own indent, which YAML allows
// under a mapping key.
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

// mapping reads the block mapping whose keys sit at indent.
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

// sequence reads the block sequence whose "-" markers sit at indent.
func (r *yamlReader) sequence(indent int) *yamlNode {
	n := &yamlNode{kind: yamlSeq}
	for {
		ind, text, ok := r.peek()
		if !ok || ind != indent || !isSeqItem(text) {
			return n
		}
		content := strings.TrimLeft(text[1:], " \t")
		if _, _, isKey := splitKey(content); isKey || isSeqItem(content) {
			// The item's content is a node at its own column, as if it
			// began on the next line.
			r.lines[r.pos] = strings.Repeat(" ", ind+len(text)-len(content)) + content
			n.items = append(n.items, r.nested(ind, false))
			continue
		}
		r.pos++
		n.items = append(n.items, r.value(content, ind, false))
	}
}

// value reads the value that follows a key or an item marker at indent
// owner: rest of that line when it holds one, the lines below otherwise.
func (r *yamlReader) value(rest string, owner int, compact bool) *yamlNode {
	if stripped, err := stripYAMLComment(rest); err == nil {
		rest = stripped
	}
	rest = strings.TrimSpace(stripProperties(strings.TrimSpace(rest)))
	if rest == "" {
		return r.nested(owner, compact)
	}
	return r.inline(rest, owner)
}

// inline reads a value that starts on the current line, together with the
// lines that continue it.
func (r *yamlReader) inline(text string, owner int) *yamlNode {
	switch text[0] {
	case '|', '>':
		return r.blockScalar(text, owner)
	case '*':
		r.skipDeeper(owner)
		return &yamlNode{kind: yamlOpaque, alias: true}
	case '@', '`':
		// YAML reserves these at the start of a plain scalar, so a real
		// parser refuses the value unless it is quoted.
		r.skipDeeper(owner)
		return &yamlNode{kind: yamlOpaque, reserved: text[0]}
	case '[', '{':
		return r.flowCollection(text, owner)
	case '"', '\'':
		text = r.joinContinuation(text, owner, false, func(s string) bool {
			_, err := readYAMLScalar(s)
			return err == nil
		})
		defer r.skipDeeper(owner)
		v, err := readYAMLScalar(text)
		if err != nil {
			return &yamlNode{kind: yamlOpaque}
		}
		return &yamlNode{kind: yamlScalar, value: v, str: true}
	}
	return r.plainScalar(text, owner)
}

// plainScalar reads an unquoted scalar and the lines indented deeper than
// owner that continue it, each joined with a blank as YAML folds them. A
// continuation line holding a key is not a scalar YAML reads, so the value
// is opaque.
func (r *yamlReader) plainScalar(text string, owner int) *yamlNode {
	parts := []string{text}
	opaque := false
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
		parts = append(parts, strings.TrimSpace(next))
	}
	if opaque {
		return &yamlNode{kind: yamlOpaque}
	}
	return &yamlNode{kind: yamlScalar, value: strings.Join(parts, " ")}
}

// blockScalar reads the body of a "|" or ">" scalar whose header is header:
// every following line indented deeper than owner, blank lines included. A
// literal scalar keeps its line breaks and a folded one joins its lines
// with blanks. The final line break is kept once, dropped under "-" and
// kept with every trailing blank line under "+".
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

// foldLines joins the lines of a folded block scalar: a break between two
// lines of text becomes a blank, a run of blank lines becomes that many
// line breaks, and a more indented line keeps its breaks.
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

// joinContinuation appends the lines indented deeper than owner to text
// until done reports the text complete. stripComments drops each joined
// line's comment, which a flow collection allows and a quoted scalar does
// not hold.
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

// flowCollection reads a "[a, b]" or "{a: b}" collection, which may run
// over several lines and nest. One the reader cannot close, or text after
// its closing bracket, is opaque.
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

// flowClose returns the index of the bracket closing the flow collection
// text opens, or -1 when it is not closed. A comment on a joined line ends
// at that line's end, so text is read with comments already removed.
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

// flowParser reads one flow collection from s. failed marks text it cannot
// read, which makes the whole collection opaque.
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

// node reads the flow node at the cursor: a collection, an alias or a
// scalar, after any anchor or tag.
func (p *flowParser) node() *yamlNode {
	p.skipBlanks()
	for p.i < len(p.s) && (p.s[p.i] == '&' || p.s[p.i] == '!') {
		for p.i < len(p.s) && strings.IndexByte(" \t,[]{}", p.s[p.i]) < 0 {
			p.i++
		}
		p.skipBlanks()
	}
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

// plain reads an unquoted flow scalar up to the indicator that ends it.
// asKey also ends it at a colon followed by a blank or an indicator.
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

// quoted reads a quoted scalar starting at the cursor.
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

// sequence reads "[a, b]" at the cursor.
func (p *flowParser) sequence() *yamlNode {
	n := &yamlNode{kind: yamlSeq, flow: true}
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

// mapping reads "{a: b, c: d}" at the cursor.
func (p *flowParser) mapping() *yamlNode {
	n := &yamlNode{kind: yamlMap, flow: true}
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

// separator consumes the comma after an entry, or reports whether the
// closing bracket follows, leaving it for the caller.
func (p *flowParser) separator(closing byte) bool {
	p.skipBlanks()
	if p.i < len(p.s) && p.s[p.i] == ',' {
		p.i++
		return true
	}
	return p.i < len(p.s) && p.s[p.i] == closing
}

// isSeqItem reports whether text starts a block sequence item.
func isSeqItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ") || strings.HasPrefix(text, "-\t")
}

// splitKey splits a "key: value" line into its key and the text after the
// colon. A key is a quoted scalar or a plain one that does not start with a
// YAML indicator, followed by a colon that ends the line or precedes a blank.
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

// stripProperties drops a leading anchor ("&name") and tag ("!tag") from a
// value, which name or type it without changing what the reader takes.
func stripProperties(s string) string {
	for s != "" && (s[0] == '&' || s[0] == '!') {
		end := strings.IndexAny(s, " \t")
		if end < 0 {
			return ""
		}
		s = strings.TrimLeft(s[end:], " \t")
	}
	return s
}

// readYAMLScalar reads s as one flow scalar: surrounding blanks removed, and
// the quotes and escapes of a quoted value decoded. A quote left open, or
// text after the closing quote, is an error.
func readYAMLScalar(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	switch s[0] {
	case '"':
		return strconv.Unquote(s)
	case '\'':
		if len(s) < 2 || s[len(s)-1] != '\'' {
			return "", errors.New("unterminated single-quoted scalar")
		}
		inner := s[1 : len(s)-1]
		if strings.Contains(strings.ReplaceAll(inner, "''", ""), "'") {
			return "", errors.New("unescaped quote inside single-quoted scalar")
		}
		return strings.ReplaceAll(inner, "''", "'"), nil
	}
	return s, nil
}

// stripYAMLComment returns s up to the "#" that starts a comment: one at the
// start of s or after a blank, outside a quoted scalar. A quote opens a
// scalar only at the start of a token. An unterminated quote is an error.
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

// skipQuotedYAML skips over the byte at s[i] inside a scalar opened by
// *quote and returns the index to continue from. It consumes a backslash
// escape in a double-quoted scalar and a doubled single quote in a
// single-quoted one, and clears *quote when s[i] closes the scalar.
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
