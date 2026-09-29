package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
)

// manifestSuite checks the names target's manifest declares against the
// kit's resource registry: every spec.outputs name, and every
// spec.requires[].reads name together with its requirement's capability.
// Given a base manifest (the --base-manifest path), it also asks a changed
// spec.version for a release class and a release note. It reads nothing
// else of the manifest, so a field it does not know is never refused.
func manifestSuite(target, baseManifest string) SuiteResult {
	const name = "manifest"

	path := filepath.Join(target, manifestFile)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return SuiteResult{name, "skipped", fmt.Sprintf(
			"%s has no %s (trust-plumbing reports that), so there are no outputs or reads to check", target, manifestFile)}
	}
	m, err := readBlockManifest(path)
	if err != nil {
		return SuiteResult{name, "fail", err.Error()}
	}

	problems := registryProblems(m)
	note, problem, err := compareVersion(m, baseManifest)
	switch {
	case err != nil:
		problems = append(problems, err.Error())
	case problem != "":
		problems = append(problems, problem)
	}
	if len(problems) > 0 {
		return SuiteResult{name, "fail", path + ": " + strings.Join(problems, "; ")}
	}
	return SuiteResult{name, "pass",
		"spec.outputs and spec.requires[].reads resolve through the resource registry; " + note}
}

// blockManifest is what the manifest suite reads of a manifest.
type blockManifest struct {
	version      string
	releaseClass string
	releaseNote  string
	outputs      []string
	requires     []requirement
}

// requirement is one item of spec.requires.
type requirement struct {
	capability string
	reads      []string
}

// registryProblems returns one message per output or read name the registry
// does not resolve, and per read name its requirement's capability does not
// give.
func registryProblems(m blockManifest) []string {
	var problems []string
	for _, out := range m.outputs {
		if out == "" {
			problems = append(problems, "spec.outputs: a name must not be empty")
			continue
		}
		if _, ok := contract.LookupResource(out); !ok {
			problems = append(problems, fmt.Sprintf(
				"spec.outputs: %q is not in the resource registry (nearest registered name: %q)", out, contract.NearestResource(out)))
		}
	}
	for _, r := range m.requires {
		for _, read := range r.reads {
			if msg := readProblem(r.capability, read); msg != "" {
				problems = append(problems, msg)
			}
		}
	}
	return problems
}

// readProblem judges one spec.requires[].reads name as the platform does at
// apply: it must be non-empty, resolve through the registry, and name an
// entry that lists capability.
func readProblem(capability, name string) string {
	if name == "" {
		return fmt.Sprintf("spec.requires[%s].reads: a name must not be empty", capability)
	}
	entry, ok := contract.LookupResource(name)
	if !ok {
		return fmt.Sprintf("spec.requires[%s].reads: %q is not in the resource registry (nearest registered name: %q)",
			capability, name, contract.NearestResource(name))
	}
	for _, c := range entry.Capabilities {
		if c == capability {
			return ""
		}
	}
	return fmt.Sprintf("spec.requires[%s].reads: %q is not an output of capability %q", capability, name, capability)
}

// compareVersion compares m's spec.version with the base manifest at base.
// It returns a note for the pass detail, or a problem when the version
// changed without a release class or note. No base path, or a base path
// that does not exist (a block new on this branch), compares nothing. A
// base that is present but cannot be read is an error, never an absence.
func compareVersion(m blockManifest, base string) (note, problem string, err error) {
	if base == "" {
		return "no --base-manifest given, so spec.version was not compared", "", nil
	}
	if _, statErr := os.Lstat(base); errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Sprintf("base manifest %s does not exist, so the block is new and spec.version was not compared", base), "", nil
	}
	raw, err := os.ReadFile(base)
	if err != nil {
		return "", "", fmt.Errorf("base manifest %s is present but could not be read, so spec.version was not compared: %v", base, err)
	}
	spec, err := manifestSpec(string(raw))
	if err != nil {
		return "", "", fmt.Errorf("base manifest %s: %v", base, err)
	}
	baseVersion, err := scalarField(spec, "spec.version", "version")
	if err != nil {
		return "", "", fmt.Errorf("base manifest %s: %v", base, err)
	}
	if baseVersion == m.version {
		return fmt.Sprintf("spec.version %s is unchanged from the base manifest", m.version), "", nil
	}

	var missing []string
	if m.releaseClass == "" {
		missing = append(missing, "spec.releaseClass")
	}
	if m.releaseNote == "" {
		missing = append(missing, "spec.releaseNote")
	}
	if len(missing) > 0 {
		return "", fmt.Sprintf("spec.version changed from %q in the base manifest to %q, which needs %s",
			baseVersion, m.version, strings.Join(missing, " and ")), nil
	}
	return fmt.Sprintf("spec.version changed from %s to %s with a release class and note", baseVersion, m.version), "", nil
}

// readBlockManifest reads the fields the manifest suite checks from the
// manifest at path. An error names path.
func readBlockManifest(path string) (blockManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return blockManifest{}, fmt.Errorf("%s is present but could not be read, so its outputs and reads are unknown: %v", path, err)
	}
	spec, err := manifestSpec(string(raw))
	if err != nil {
		return blockManifest{}, fmt.Errorf("%s: %v", path, err)
	}
	m, err := specFields(spec)
	if err != nil {
		return blockManifest{}, fmt.Errorf("%s: %v", path, err)
	}
	return m, nil
}

// manifestSpec parses content and returns its spec mapping, or nil when the
// manifest has no spec.
func manifestSpec(content string) (*yamlNode, error) {
	root := parseYAML(content)
	switch root.kind {
	case yamlNull:
		return nil, nil
	case yamlMap:
	default:
		return nil, unfollowable("the document")
	}
	spec := root.get("spec")
	if spec == nil || spec.kind == yamlNull {
		return nil, nil
	}
	if !spec.followableMap() {
		return nil, unfollowable("spec")
	}
	return spec, nil
}

// specFields extracts the fields the manifest suite checks from spec.
func specFields(spec *yamlNode) (blockManifest, error) {
	var m blockManifest
	var err error
	if m.version, err = scalarField(spec, "spec.version", "version"); err != nil {
		return m, err
	}
	if m.releaseClass, err = scalarField(spec, "spec.releaseClass", "releaseClass"); err != nil {
		return m, err
	}
	if m.releaseNote, err = scalarField(spec, "spec.releaseNote", "releaseNote"); err != nil {
		return m, err
	}
	if m.outputs, err = nameList(spec.get("outputs"), "spec.outputs"); err != nil {
		return m, err
	}

	requires := spec.get("requires")
	switch {
	case requires == nil || requires.kind == yamlNull:
		return m, nil
	case requires.kind != yamlSeq:
		return m, unfollowable("spec.requires")
	}
	for _, item := range requires.items {
		if !item.followableMap() {
			return m, unfollowable("an item of spec.requires")
		}
		capability, err := scalarField(item, "spec.requires[].capability", "capability")
		if err != nil {
			return m, err
		}
		reads, err := nameList(item.get("reads"), fmt.Sprintf("spec.requires[%s].reads", capability))
		if err != nil {
			return m, err
		}
		m.requires = append(m.requires, requirement{capability, reads})
	}
	return m, nil
}

// scalarField returns the trimmed scalar under key in n, or "" when the key
// is absent or empty. field names it in an error.
func scalarField(n *yamlNode, field, key string) (string, error) {
	if n == nil {
		return "", nil
	}
	v := n.get(key)
	switch {
	case v == nil || v.kind == yamlNull:
		return "", nil
	case v.kind == yamlScalar:
		return strings.TrimSpace(v.value), nil
	}
	return "", unfollowable(field)
}

// nameList reads n as a list of names: absent and empty give none.
func nameList(n *yamlNode, field string) ([]string, error) {
	if n == nil || n.kind == yamlNull {
		return nil, nil
	}
	if n.kind != yamlSeq {
		return nil, unfollowable(field)
	}
	names := make([]string, 0, len(n.items))
	for _, item := range n.items {
		switch item.kind {
		case yamlNull:
			names = append(names, "")
		case yamlScalar:
			names = append(names, strings.TrimSpace(item.value))
		default:
			return nil, unfollowable("an item of " + field)
		}
	}
	return names, nil
}

func unfollowable(field string) error {
	return fmt.Errorf("%s is in a form the reader cannot follow (a flow mapping, an alias, a merge key or a misplaced line), so what it declares is unknown", field)
}

// yamlKind is the kind of a node parseYAML builds.
type yamlKind int

const (
	yamlNull yamlKind = iota
	yamlScalar
	yamlMap
	yamlSeq
	// yamlOpaque is a flow mapping, an alias, a nested flow collection or a
	// quoted scalar the reader cannot close.
	yamlOpaque
)

// yamlNode is one node of the YAML subset parseYAML reads.
type yamlNode struct {
	kind  yamlKind
	value string
	keys  []string
	vals  []*yamlNode
	items []*yamlNode
	// misplaced marks a mapping holding a line that is neither a key of it
	// nor inside one of its values.
	misplaced bool
}

// get returns the value under key in a mapping, or nil.
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
	return n.kind == yamlMap && !n.misplaced && n.get("<<") == nil
}

// parseYAML reads content by indentation into block mappings, block
// sequences, flow sequences of scalars and scalars, including block scalars.
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
// stepping over blank lines, comment lines and document markers.
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

// skipDeeper steps over every line indented deeper than owner: the rest of
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

// inline reads a value that starts on the current line, then steps over the
// lines that continue it.
func (r *yamlReader) inline(text string, owner int) *yamlNode {
	switch text[0] {
	case '|', '>':
		return r.blockScalar(owner)
	case '*', '{':
		r.skipDeeper(owner)
		return &yamlNode{kind: yamlOpaque}
	case '[':
		return r.flowSequence(text, owner)
	case '"', '\'':
		text = r.joinContinuation(text, owner, false, func(s string) bool {
			_, err := readYAMLScalar(s)
			return err == nil
		})
	}
	defer r.skipDeeper(owner)
	v, err := readYAMLScalar(text)
	if err != nil {
		return &yamlNode{kind: yamlOpaque}
	}
	return &yamlNode{kind: yamlScalar, value: v}
}

// blockScalar reads the body of a "|" or ">" scalar: every following line
// indented deeper than owner, blank lines included.
func (r *yamlReader) blockScalar(owner int) *yamlNode {
	var body []string
	for ; r.pos < len(r.lines); r.pos++ {
		line := strings.TrimSuffix(r.lines[r.pos], "\r")
		text := strings.TrimLeft(line, " \t")
		if text != "" && len(line)-len(text) <= owner {
			break
		}
		body = append(body, text)
	}
	return &yamlNode{kind: yamlScalar, value: strings.TrimSpace(strings.Join(body, "\n"))}
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

// flowSequence reads a "[a, b]" list of scalars, which may run over several
// lines. A nested collection, or text after the closing bracket, is opaque.
func (r *yamlReader) flowSequence(text string, owner int) *yamlNode {
	text = r.joinContinuation(text, owner, true, func(s string) bool { return flowClose(s) >= 0 })
	r.skipDeeper(owner)
	end := flowClose(text)
	if end < 0 || strings.TrimSpace(text[end+1:]) != "" {
		return &yamlNode{kind: yamlOpaque}
	}
	inner := text[1:end]
	if strings.TrimSpace(inner) == "" {
		return &yamlNode{kind: yamlSeq}
	}
	n := &yamlNode{kind: yamlSeq}
	parts := splitOutsideQuotes(inner, ',')
	if len(parts) > 1 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	for _, p := range parts {
		v, err := readYAMLScalar(p)
		if err != nil {
			return &yamlNode{kind: yamlOpaque}
		}
		n.items = append(n.items, &yamlNode{kind: yamlScalar, value: v})
	}
	return n
}

// flowClose returns the index of the "]" closing the flow sequence text
// opens, or -1 when it is not closed or holds a nested collection. A
// comment on a joined line ends at that line's end, so text is read with
// comments already removed.
func flowClose(text string) int {
	var quote byte
	for i := 1; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			i = skipQuotedYAML(text, i, &quote)
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '[', '{':
			return -1
		case ']':
			return i
		}
	}
	return -1
}

// splitOutsideQuotes splits s on sep wherever sep is outside a quoted scalar.
func splitOutsideQuotes(s string, sep byte) []string {
	var parts []string
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			i = skipQuotedYAML(s, i, &quote)
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case sep:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
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
			if i == 0 || strings.IndexByte(" \t[,-", s[i-1]) >= 0 {
				quote = c
			}
		}
	}
	if quote != 0 {
		return "", errors.New("unterminated quoted scalar")
	}
	return s, nil
}

// skipQuotedYAML steps over the byte at s[i] inside a scalar opened by
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
