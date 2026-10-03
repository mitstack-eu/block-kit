package contract

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// splitProperties splits a leading anchor ("&name") and tag ("!tag") from a
// value, returning the value and the tag, "" when it has none. An anchor
// only names the value, so it is dropped.
func splitProperties(s string) (rest, tag string) {
	for s != "" && (s[0] == '&' || s[0] == '!') {
		end := strings.IndexAny(s, " \t")
		if end < 0 {
			end = len(s)
		}
		if s[0] == '!' {
			tag = s[:end]
		}
		s = strings.TrimLeft(s[end:], " \t")
	}
	return s, tag
}

// coreTag returns the name of the YAML core tag tag spells, as in "str" for
// !!str or !<tag:yaml.org,2002:str>, or "" for any other tag.
func coreTag(tag string) string {
	if name, ok := strings.CutPrefix(tag, "!!"); ok {
		return name
	}
	if name, ok := strings.CutPrefix(tag, "!<tag:yaml.org,2002:"); ok {
		return strings.TrimSuffix(name, ">")
	}
	return ""
}

// yamlSpecialFloat is the YAML spelling of an infinity or not-a-number.
var yamlSpecialFloat = regexp.MustCompile(`^([-+]?\.(inf|Inf|INF)|\.(nan|NaN|NAN))$`)

// resolvedTag returns the core tag YAML resolves the untagged plain scalar
// text to: null, bool, int, float or str.
func resolvedTag(text string) string {
	plain := strings.ReplaceAll(text, "_", "")
	switch {
	case text == "" || plainNull.MatchString(text):
		return "null"
	case plainBoolean.MatchString(text):
		return "bool"
	case yamlIntegerText(plain):
		return "int"
	case yamlStyleFloat.MatchString(plain) || yamlSpecialFloat.MatchString(text):
		return "float"
	}
	return "str"
}

// yamlIntegerText reports whether plain, its underscores dropped, is an
// integer YAML reads: decimal, or with a 0x, 0o or 0b prefix.
func yamlIntegerText(plain string) bool {
	if _, err := strconv.ParseInt(plain, 0, 64); err == nil {
		return true
	}
	_, err := strconv.ParseUint(plain, 0, 64)
	return err == nil
}

// withTag applies the tag a value was written with to n, as yaml.v3 applies
// it. A string tag makes a scalar a string whatever its text, and a value
// left empty the empty string. A null, bool, int or float tag reads the
// scalar, quoted or not, as YAML reads the plain text, which must resolve
// to that tag, or to an int under a float tag; a scalar that does not is
// refused. A binary tag is refused, since the reader does not decode
// base64. Any other tag, and a tag on a collection, leaves n as it is.
func withTag(n *yamlNode, tag string) *yamlNode {
	name := coreTag(tag)
	if name == "" || n.kind != yamlNull && n.kind != yamlScalar {
		return n
	}
	switch name {
	case "str":
		return &yamlNode{kind: yamlScalar, value: n.value, str: true}
	case "binary":
		return &yamlNode{kind: yamlOpaque, tag: tag, tagNote: "a base64 value the kit's YAML reader does not decode"}
	case "null", "bool", "int", "float":
	default:
		return n
	}
	got := resolvedTag(n.value)
	switch {
	case got == name, name == "float" && got == "int":
	default:
		return &yamlNode{kind: yamlOpaque, tag: tag, tagNote: strconv.Quote(n.value) + " is not a value of that tag"}
	}
	if got == "null" {
		return &yamlNode{kind: yamlNull}
	}
	return &yamlNode{kind: yamlScalar, value: n.value}
}

// readYAMLScalar reads s as one flow scalar: surrounding blanks removed, and
// the quotes and escapes of a quoted value decoded as YAML decodes them. A
// quote left open, an escape YAML does not define, or text after the
// closing quote, is an error.
func readYAMLScalar(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '"' && s[0] != '\'' {
		return s, nil
	}
	v, end, ok := scanQuoted(s)
	if !ok || end != len(s) {
		return "", errors.New("not one quoted scalar")
	}
	return v, nil
}

// yamlEscapes are the one-character escapes of a double-quoted scalar.
var yamlEscapes = map[byte]string{
	'0': "\x00", 'a': "\x07", 'b': "\x08", 't': "\t", '\t': "\t", 'n': "\n", 'v': "\x0b",
	'f': "\x0c", 'r': "\r", 'e': "\x1b", ' ': " ", '"': "\"", '\'': "'", '\\': "\\",
	'N': "\u0085", '_': " ", 'L': " ", 'P': " ",
}

// yamlHexEscapes are the escapes followed by a hex code, by its length.
var yamlHexEscapes = map[byte]int{'x': 2, 'u': 4, 'U': 8}

// scanQuoted reads the single- or double-quoted scalar s starts with, which
// may run over several lines, as yaml.v3's scanner reads it. Blanks around
// a line break are dropped; one line break folds to a blank and each
// further one is kept; a double-quoted scalar decodes its escapes and drops
// a line break escaped with a backslash. It returns the value and the index
// just past the closing quote. ok is false when s leaves the scalar open,
// holds an escape YAML does not define, or holds a document marker at the
// start of a line.
func scanQuoted(s string) (value string, end int, ok bool) {
	single := s[0] == '\''
	closing := s[0]
	isBlank := func(i int) bool { return i < len(s) && (s[i] == ' ' || s[i] == '\t') }
	isBreak := func(i int) bool { return i < len(s) && s[i] == '\n' }
	var out, whitespaces []byte
	leadingBreak, trailingBreaks := 0, 0
	i := 1
	for {
		if i >= len(s) || s[i-1] == '\n' && atDocumentMarker(s[i:]) {
			return "", 0, false
		}
		leadingBlanks := false
	text:
		for i < len(s) && !isBlank(i) && !isBreak(i) {
			c := s[i]
			switch {
			case single && c == '\'' && i+1 < len(s) && s[i+1] == '\'':
				out = append(out, '\'')
				i += 2
			case c == closing:
				break text
			case !single && c == '\\' && isBreak(i+1):
				i += 2
				leadingBlanks = true
				break text
			case !single && c == '\\':
				if i+1 >= len(s) {
					return "", 0, false
				}
				esc := s[i+1]
				i += 2
				if v, known := yamlEscapes[esc]; known {
					out = append(out, v...)
					continue
				}
				n, known := yamlHexEscapes[esc]
				if !known || i+n > len(s) {
					return "", 0, false
				}
				code, err := strconv.ParseUint(s[i:i+n], 16, 32)
				if err != nil || code >= 0xD800 && code <= 0xDFFF || code > 0x10FFFF {
					return "", 0, false
				}
				out = utf8.AppendRune(out, rune(code))
				i += n
			default:
				out = append(out, c)
				i++
			}
		}
		if i < len(s) && s[i] == closing {
			return string(out), i + 1, true
		}
		for isBlank(i) || isBreak(i) {
			switch {
			case isBlank(i) && !leadingBlanks:
				whitespaces = append(whitespaces, s[i])
			case isBreak(i) && !leadingBlanks:
				whitespaces = whitespaces[:0]
				leadingBreak = 1
				leadingBlanks = true
			case isBreak(i):
				trailingBreaks++
			}
			i++
		}
		if leadingBlanks {
			switch {
			case leadingBreak > 0 && trailingBreaks == 0:
				out = append(out, ' ')
			default:
				out = append(out, strings.Repeat("\n", trailingBreaks)...)
			}
			leadingBreak, trailingBreaks = 0, 0
		} else {
			out = append(out, whitespaces...)
			whitespaces = whitespaces[:0]
		}
	}
}
