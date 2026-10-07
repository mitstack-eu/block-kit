package contract

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// splitProperties drops an anchor, which only names the value, and returns the tag.
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

func coreTag(tag string) string {
	if name, ok := strings.CutPrefix(tag, "!!"); ok {
		return name
	}
	if name, ok := strings.CutPrefix(tag, "!<tag:yaml.org,2002:"); ok {
		return strings.TrimSuffix(name, ">")
	}
	return ""
}

var yamlSpecialFloat = regexp.MustCompile(`^([-+]?\.(inf|Inf|INF)|\.(nan|NaN|NAN))$`)

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

func yamlIntegerText(plain string) bool {
	if _, err := strconv.ParseInt(plain, 0, 64); err == nil {
		return true
	}
	_, err := strconv.ParseUint(plain, 0, 64)
	return err == nil
}

// withTag applies a tag as yaml.v3 does; a binary tag is refused because the reader does not decode base64, and any other tag leaves n as it is.
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

var yamlEscapes = map[byte]string{
	'0': "\x00", 'a': "\x07", 'b': "\x08", 't': "\t", '\t': "\t", 'n': "\n", 'v': "\x0b",
	'f': "\x0c", 'r': "\r", 'e': "\x1b", ' ': " ", '"': "\"", '\'': "'", '\\': "\\",
	'N': "\u0085", '_': " ", 'L': " ", 'P': " ",
}

var yamlHexEscapes = map[byte]int{'x': 2, 'u': 4, 'U': 8}

// scanQuoted reads a possibly multi-line quoted scalar as yaml.v3's scanner does and returns the index just past the closing quote.
//
// Blanks around a line break are dropped; one break folds to a blank and each further one is kept; a backslash-escaped break is dropped. ok is false when the scalar is left open, holds an undefined escape, or holds a document marker at the start of a line.
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
