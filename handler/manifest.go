package handler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
)

// errNilResource is returned by New when Config.Resource is nil.
var errNilResource = errors.New("handler: Config.Resource is required")

// errEmptyJWKSURL is returned by New when Config.JWKSURL is empty.
// webhook.AuthenticateHook rejects every request on an empty JWKSURL before
// it ever consults Config.PublicKey, so PublicKey is never a substitute for
// it.
var errEmptyJWKSURL = errors.New("handler: Config.JWKSURL is required")

// knownHooks is the closed set of hook points New will ever accept a
// registration for. It comes from the contract so the handler and the
// platform's manifest validation share one set.
var knownHooks = contract.LifecycleHookPoints()

// validateHooks refuses a Config that would let a block override an
// invariant through Hooks: a registration under a name that is not one of
// the four HookXxx constants, a registration for a hook the manifest does
// not declare, a non-empty Hooks with no Manifest to check it against, or a
// manifest whose spec.lifecycle.hooks value fits no form manifestHooks can
// read, names a hook outside knownHooks, or names one hook twice.
//
// The manifest is checked whenever it is non-empty, even with no Hooks
// registered at all, so that New refuses exactly the declarations the
// platform's manifest validation refuses. A declaration nobody can read is
// refused rather than treated as declaring nothing.
func validateHooks(cfg Config) error {
	if len(cfg.Hooks) > 0 && len(cfg.Manifest) == 0 {
		return errors.New("handler: Config.Hooks is set but Config.Manifest is empty — a hook can only be registered if the manifest declares it")
	}
	if len(cfg.Manifest) == 0 {
		return nil
	}

	declared, err := manifestHooks(cfg.Manifest)
	if err != nil {
		return err
	}

	knownSet := make(map[string]bool, len(knownHooks))
	for _, h := range knownHooks {
		knownSet[h] = true
	}
	declaredSet := make(map[string]bool, len(declared))
	for _, h := range declared {
		if !knownSet[h] {
			return fmt.Errorf("handler: manifest's spec.lifecycle.hooks declares unknown hook point %q; the known hook points are %s",
				h, strings.Join(knownHooks, ", "))
		}
		if declaredSet[h] {
			return fmt.Errorf("handler: manifest's spec.lifecycle.hooks declares hook %q more than once", h)
		}
		declaredSet[h] = true
	}

	for name := range cfg.Hooks {
		if !knownSet[name] {
			return fmt.Errorf("handler: Config.Hooks registers unknown hook point %q; the known hook points are %s",
				name, strings.Join(knownHooks, ", "))
		}
		if !declaredSet[name] {
			return fmt.Errorf("handler: Config.Hooks registers hook %q, but the manifest's spec.lifecycle.hooks does not declare it (declares: %s)",
				name, strings.Join(declared, ", "))
		}
	}
	return nil
}

// manifestHooks reads the hook names a block manifest declares under
// spec.lifecycle.hooks: spec: at the document's top level, lifecycle:
// nested exactly one level under it, hooks: nested exactly one level under
// that. A hooks: key anywhere else — a decoy above spec.lifecycle, or one
// nested deeper under some other spec.lifecycle child — is ignored, since
// only the one anchored at that exact path counts.
//
// The kit takes no YAML dependency, so this reads the manifest as plain
// text, anchoring purely on indentation. Three hooks: forms are read: the
// block sequence
//
//	hooks:
//	  - beforeEnsure
//	  - afterDelete
//
// the flow list "hooks: [beforeEnsure, afterDelete]", and "hooks: []",
// which — like a manifest with no hooks: key at all — declares no hook.
// Each name is read as a YAML scalar: plain, "double-quoted" or
// 'single-quoted', with the quotes removed. A comment is stripped wherever
// YAML allows one: after the hooks: key, after a sequence item, after the
// closing "]" of a flow list, and on a line of its own between items. A "#"
// starts a comment only at the start of the text or after whitespace, and
// never inside quotes, so "before#Ensure" in quotes is read whole. A
// hooks: value that fits none of those forms makes manifestHooks return an
// error instead of silently declaring nothing, since a declaration nobody
// can read must not be mistaken for an empty one.
func manifestHooks(manifest []byte) ([]string, error) {
	lines := strings.Split(string(manifest), "\n")

	specLine := findSpec(lines)
	if specLine < 0 {
		return nil, nil
	}

	lifecycleLine := findChild(lines, specLine, 0, "lifecycle:", false)
	if lifecycleLine < 0 {
		return nil, nil
	}

	hooksLine := findChild(lines, lifecycleLine, leadingSpaces(lines[lifecycleLine]), "hooks:", true)
	if hooksLine < 0 {
		return nil, nil
	}

	return parseHooksValue(lines, hooksLine)
}

// manifestRetainsOnUnsubscribe reads spec.lifecycle.retainsOnUnsubscribe,
// anchored the same way manifestHooks anchors spec.lifecycle.hooks: spec: at
// the document's top level, lifecycle: a direct child of it, and
// retainsOnUnsubscribe: a direct child of lifecycle, found with the same
// findChild helper. A retainsOnUnsubscribe: key anywhere else — a decoy
// above spec.lifecycle, or nested deeper under some other spec.lifecycle
// child — is ignored.
//
// No manifest, no spec.lifecycle, or no retainsOnUnsubscribe key under it
// all mean false. Only a bare "true" or "false" value is read; anything
// else makes manifestRetainsOnUnsubscribe return an error naming the field,
// the same way manifestHooks refuses a hooks: value it cannot read, since a
// declaration nobody can read must not be mistaken for false.
func manifestRetainsOnUnsubscribe(manifest []byte) (bool, error) {
	if len(manifest) == 0 {
		return false, nil
	}
	lines := strings.Split(string(manifest), "\n")

	specLine := findSpec(lines)
	if specLine < 0 {
		return false, nil
	}

	lifecycleLine := findChild(lines, specLine, 0, "lifecycle:", false)
	if lifecycleLine < 0 {
		return false, nil
	}

	retainsLine := findChild(lines, lifecycleLine, leadingSpaces(lines[lifecycleLine]), "retainsOnUnsubscribe:", true)
	if retainsLine < 0 {
		return false, nil
	}

	trimmed := strings.TrimSpace(lines[retainsLine])
	rest, ok := strings.CutPrefix(trimmed, "retainsOnUnsubscribe:")
	if ok {
		switch strings.TrimSpace(rest) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, fmt.Errorf("handler: manifest's spec.lifecycle.retainsOnUnsubscribe must be a bare true or false: %q", trimmed)
}

// ManifestAppNameNaming reads a block manifest's spec.naming.appName
// declaration (contract.AppNameNamingField), anchored the same way
// manifestHooks anchors spec.lifecycle.hooks: spec: at the document's top
// level, naming: a direct child of it, appName: a direct child of naming.
// The declaration is read as
//
//	appName:
//	  maxLength: 27
//	  derivedNames:
//	    - template: "mitstack-app-{appName}-github-actions-ecr-push"
//	      limit: 64
//
// with each value read as a YAML scalar, quotes removed. A manifest with no
// spec.naming.appName returns nil and no error. A maxLength with no
// derivedNames key returns a declaration whose DerivedNames is nil. Any
// value the reader cannot read, including naming: or appName: written as a
// flow mapping, a missing maxLength, a non-integer maxLength or limit, and a
// derivedNames item with a key other than template and limit or without
// both of them, is an error naming the field, since a declaration nobody
// can read must not be mistaken for an absent one.
func ManifestAppNameNaming(manifest []byte) (*contract.AppNameNaming, error) {
	lines := strings.Split(string(manifest), "\n")

	specLine := findSpec(lines)
	if specLine < 0 {
		return nil, nil
	}
	namingLine := findChild(lines, specLine, 0, "naming:", true)
	if namingLine < 0 {
		return nil, nil
	}
	if err := requireBlockKey(lines[namingLine], "naming:"); err != nil {
		return nil, err
	}
	appNameLine := findChild(lines, namingLine, leadingSpaces(lines[namingLine]), "appName:", true)
	if appNameLine < 0 {
		return nil, nil
	}
	if err := requireBlockKey(lines[appNameLine], "appName:"); err != nil {
		return nil, err
	}
	appNameIndent := leadingSpaces(lines[appNameLine])

	maxLengthLine := findChild(lines, appNameLine, appNameIndent, "maxLength:", true)
	if maxLengthLine < 0 {
		return nil, unreadableNaming("appName: declares no maxLength")
	}
	maxLength, err := readIntValue(lines[maxLengthLine], "maxLength:")
	if err != nil {
		return nil, err
	}
	naming := &contract.AppNameNaming{MaxLength: maxLength}

	derivedLine := findChild(lines, appNameLine, appNameIndent, "derivedNames:", true)
	if derivedLine < 0 {
		return naming, nil
	}
	if err := requireBlockKey(lines[derivedLine], "derivedNames:"); err != nil {
		return nil, err
	}
	naming.DerivedNames, err = parseDerivedNames(lines, derivedLine)
	if err != nil {
		return nil, err
	}
	return naming, nil
}

// parseDerivedNames reads the block sequence under the derivedNames: line at
// lines[keyLine]. Each item is a mapping of template and limit, its first
// key on the "- " line and the other on a line of its own. The sequence may
// sit at the key's own indent, as YAML allows; any other line at or below
// that indent ends it. An item missing template or limit is an error naming
// the missing key.
func parseDerivedNames(lines []string, keyLine int) ([]contract.DerivedName, error) {
	keyIndent := leadingSpaces(lines[keyLine])
	var names []contract.DerivedName
	var seen []map[string]bool
	for _, line := range lines[keyLine+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if leadingWhitespaceHasTab(line) {
			return nil, unreadableNaming(trimmed)
		}
		indent := leadingSpaces(line)
		if indent < keyIndent || (indent == keyIndent && !isSequenceItem(trimmed)) {
			break
		}
		entry := trimmed
		if isSequenceItem(trimmed) {
			names = append(names, contract.DerivedName{})
			seen = append(seen, map[string]bool{})
			entry = strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		}
		if len(names) == 0 {
			return nil, unreadableNaming(trimmed)
		}
		key, value, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, unreadableNaming(trimmed)
		}
		current := &names[len(names)-1]
		switch key {
		case "template":
			template, err := readScalar(value)
			if err != nil {
				return nil, unreadableNaming(trimmed)
			}
			current.Template = template
		case "limit":
			limit, err := readInt(value)
			if err != nil {
				return nil, unreadableNaming(trimmed)
			}
			current.Limit = limit
		default:
			return nil, unreadableNaming(trimmed)
		}
		seen[len(seen)-1][key] = true
	}
	for i, keys := range seen {
		for _, key := range []string{"template", "limit"} {
			if !keys[key] {
				return nil, fmt.Errorf("handler: manifest's %s: derivedNames item %d has no %s",
					contract.AppNameNamingField, i+1, key)
			}
		}
	}
	return names, nil
}

// requireBlockKey returns an error unless line, with any trailing comment
// removed, is exactly key: a key opening a nested block, not one written
// with an inline value or flow mapping.
func requireBlockKey(line, key string) error {
	trimmed := strings.TrimSpace(line)
	bare, err := stripComment(trimmed)
	if err != nil || strings.TrimSpace(bare) != key {
		return unreadableNaming(trimmed)
	}
	return nil
}

// readIntValue reads the integer value of the "key: value" line.
func readIntValue(line, key string) (int, error) {
	trimmed := strings.TrimSpace(line)
	n, err := readInt(strings.TrimPrefix(trimmed, key))
	if err != nil {
		return 0, unreadableNaming(trimmed)
	}
	return n, nil
}

// readInt reads s as one YAML scalar holding a decimal integer.
func readInt(s string) (int, error) {
	v, err := readScalar(s)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

// unreadableNaming is the error ManifestAppNameNaming returns for text, in
// the spec.naming.appName declaration, that it cannot read.
func unreadableNaming(text string) error {
	return fmt.Errorf("handler: manifest's %s is not a form the kit can read: %q", contract.AppNameNamingField, text)
}

// findSpec returns the index of the top-level spec: line, or -1 when the
// manifest has none. The line must read exactly spec: once any trailing
// comment is stripped.
func findSpec(lines []string) int {
	for i, line := range lines {
		if leadingSpaces(line) != 0 {
			continue
		}
		if key, err := stripComment(line); err == nil && strings.TrimSpace(key) == "spec:" {
			return i
		}
	}
	return -1
}

// findChild scans forward from just after the line at parentLine (itself
// indented by parentIndent) for a direct child of that line: the first
// line indented more than parentIndent fixes the child indent level, and
// only a line at exactly that level counts, since anything indented deeper
// belongs to that child's own nested block, not to the parent directly.
// The scan stops as soon as indentation drops back to parentIndent or
// below, since that ends the parent's block. A blank line or a comment-only
// line is skipped at any indentation, since YAML ignores both, so neither
// ends the block nor fixes the child indent level. It returns the index of
// the first direct-child line whose trimmed text equals name or, when
// prefixOK is true, starts with name — or -1 when there is none.
func findChild(lines []string, parentLine, parentIndent int, name string, prefixOK bool) int {
	childIndent := -1
	for i := parentLine + 1; i < len(lines); i++ {
		line := lines[i]
		if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(line)
		if indent <= parentIndent {
			break
		}
		if childIndent < 0 {
			childIndent = indent
		}
		if indent != childIndent {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == name || (prefixOK && strings.HasPrefix(trimmed, name)) {
			return i
		}
	}
	return -1
}

// parseHooksValue reads the hooks: line at lines[hooksLine] in whichever of
// the three supported forms it is written, or returns an error naming the
// manifest when it fits none of them. A comment after the hooks: key or
// after a flow list's closing "]" is ignored, and every name is read through
// readScalar, so quoting and trailing comments never reach the name.
func parseHooksValue(lines []string, hooksLine int) ([]string, error) {
	line := lines[hooksLine]
	trimmed := strings.TrimSpace(line)
	indent := leadingSpaces(line)

	key, err := stripComment(trimmed)
	if err != nil {
		return nil, unreadableHooks(trimmed)
	}
	key = strings.TrimSpace(key)

	if key == "hooks:" {
		var hooks []string
		for _, next := range lines[hooksLine+1:] {
			item := strings.TrimSpace(next)
			if item == "" {
				continue
			}
			if strings.HasPrefix(item, "#") {
				continue
			}
			if leadingWhitespaceHasTab(next) {
				return nil, unreadableHooks(item)
			}
			// YAML lets a block sequence sit at its key's own indent, so an
			// item there still belongs to hooks; any other line at or below
			// that indent ends the sequence.
			itemIndent := leadingSpaces(next)
			if itemIndent < indent || (itemIndent == indent && !isSequenceItem(item)) {
				break
			}
			if !isSequenceItem(item) {
				break
			}
			name, err := readScalar(strings.TrimPrefix(item, "-"))
			if err != nil {
				return nil, unreadableHooks(item)
			}
			hooks = append(hooks, name)
		}
		return hooks, nil
	}

	if rest, ok := strings.CutPrefix(key, "hooks:"); ok {
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "[") && strings.HasSuffix(rest, "]") {
			inner := strings.TrimSpace(rest[1 : len(rest)-1])
			if inner == "" {
				return nil, nil
			}
			items, err := splitFlowItems(inner)
			if err != nil {
				return nil, unreadableHooks(trimmed)
			}
			hooks := make([]string, 0, len(items))
			for _, it := range items {
				name, err := readScalar(it)
				if err != nil {
					return nil, unreadableHooks(trimmed)
				}
				hooks = append(hooks, name)
			}
			return hooks, nil
		}
	}

	return nil, unreadableHooks(trimmed)
}

// unreadableHooks is the error parseHooksValue returns for a hooks: value,
// quoted in text, that fits no form it can read.
// isSequenceItem reports whether a trimmed line is a block sequence item.
func isSequenceItem(trimmed string) bool {
	return strings.HasPrefix(trimmed, "-")
}

func unreadableHooks(text string) error {
	return fmt.Errorf("handler: manifest's spec.lifecycle.hooks is not a form New can read "+
		"(want %q, a flow list like %q, or an indented \"- name\" sequence): %q", "hooks: []", "hooks: [a, b]", text)
}

// readScalar reads s as one YAML scalar: it drops a trailing comment and
// surrounding whitespace, then removes the quotes from a "double-quoted" or
// 'single-quoted' value, decoding its escapes. A plain value is returned as
// written. A quote left open, or text after a closing quote, is an error.
func readScalar(s string) (string, error) {
	s, err := stripComment(s)
	if err != nil {
		return "", err
	}
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

// stripComment returns s up to the "#" that starts a YAML comment, or all
// of s when it has none. A "#" starts a comment only at the start of s or
// right after a space or tab, and never inside a quoted scalar. A quote
// opens a scalar only at the start of a token: the start of s, or after
// whitespace, "[", "," or "-". An unterminated quote is an error.
func stripComment(s string) (string, error) {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			i = skipQuoted(s, i, &quote)
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

// splitFlowItems splits the text between a flow list's brackets on the
// commas that sit outside quotes. A single trailing comma, which YAML
// allows, adds no item.
func splitFlowItems(inner string) ([]string, error) {
	var items []string
	var quote byte
	start := 0
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if quote != 0 {
			i = skipQuoted(inner, i, &quote)
			continue
		}
		switch c {
		case '"', '\'':
			if strings.TrimSpace(inner[start:i]) == "" {
				quote = c
			}
		case ',':
			items = append(items, inner[start:i])
			start = i + 1
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quoted scalar")
	}
	if last := inner[start:]; strings.TrimSpace(last) != "" || len(items) == 0 {
		items = append(items, last)
	}
	return items, nil
}

// skipQuoted steps over the byte at s[i] inside a scalar opened by *quote
// and returns the index the caller's loop should continue from. It consumes
// a backslash escape in a double-quoted scalar and a doubled single quote
// in a single-quoted one, and clears *quote when s[i] closes the scalar.
func skipQuoted(s string, i int, quote *byte) int {
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

// leadingSpaces counts the spaces a line is indented by.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// leadingWhitespaceHasTab reports whether line's leading run of spaces and
// tabs contains a tab. A tab is not valid YAML indentation, and
// leadingSpaces counts only spaces, so a tab-indented line would otherwise
// measure as column zero and be mistaken for the end of a block sequence.
func leadingWhitespaceHasTab(line string) bool {
	for _, r := range line {
		switch r {
		case ' ':
			continue
		case '\t':
			return true
		default:
			return false
		}
	}
	return false
}
