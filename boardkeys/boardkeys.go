// Package boardkeys derives which board keys a commit delivers. It is a
// verbatim port of platform's scripts/tracker-delivered-keys.sh: a key
// counts only when it sits in a LEADING key list in the commit subject,
// terminated by a literal ':', or in a whole-line `Jira:` trailer in the
// body. A key anywhere else — later in the subject, or in an ordinary body
// line — is prose and delivers nothing.
package boardkeys

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// keyPattern matches one board key token, exactly as the shell's
// `MIT-[0-9]+` does.
var keyPattern = regexp.MustCompile(`MIT-[0-9]+`)

// leadingKeyList matches a subject's leading key-list prefix: one or more
// keys separated by a space, `,`, `+` or `/` (a separator may itself be
// spaced), terminated by a literal `:`. Anchored to the start of the line,
// with optional leading whitespace, so a key that merely appears early is
// not mistaken for a leading one.
var leadingKeyList = regexp.MustCompile(`^\s*MIT-[0-9]+(?:(?:\s*[,+/]\s*|\s+)MIT-[0-9]+)*:`)

// jiraTrailer matches a whole `Jira:` trailer line: leading whitespace
// allowed, the keyword compared case-insensitively, anchored so a keyword
// buried in prose (or a look-alike like `Jira-refs:`) never matches.
var jiraTrailer = regexp.MustCompile(`(?im)^[ \t]*jira:.*$`)

// Subject returns the board keys a commit subject's leading key list names.
// It returns nothing unless line begins, after optional leading whitespace,
// with one or more keys terminated by a literal ':' — a key appearing after
// that colon is prose, even in a subject that also delivers.
func Subject(line string) []string {
	prefix := leadingKeyList.FindString(line)
	if prefix == "" {
		return nil
	}
	return dedupeSorted(keyPattern.FindAllString(prefix, -1))
}

// Message returns the board keys a commit's subject and body together
// deliver: Subject's leading key list, plus every key on a whole-line
// `Jira:` trailer in the body, merged, deduped and sorted numerically.
func Message(subject, body string) []string {
	var trailerKeys []string
	for _, line := range jiraTrailer.FindAllString(body, -1) {
		trailerKeys = append(trailerKeys, keyPattern.FindAllString(line, -1)...)
	}
	return Merge(Subject(subject), trailerKeys)
}

// Merge combines several key lists — such as one per commit in a range —
// into one: deduplicated and sorted numerically by the digits after the
// hyphen, the same rule Subject and Message already apply on their own. It
// is the package's single definition of that rule, for any caller that
// merges key lists drawn from more than one source.
func Merge(sets ...[]string) []string {
	var all []string
	for _, s := range sets {
		all = append(all, s...)
	}
	return dedupeSorted(all)
}

// dedupeSorted deduplicates keys and sorts them numerically by the digits
// after the hyphen — "MIT-9" before "MIT-10", where a lexical sort would put
// "MIT-10" first.
func dedupeSorted(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(keys))
	var out []string
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		return keyNumber(out[i]) < keyNumber(out[j])
	})
	return out
}

// keyNumber parses the digits after "MIT-". A key that fails to parse
// (never produced by keyPattern) sorts as 0 rather than panicking.
func keyNumber(key string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(key, "MIT-"))
	if err != nil {
		return 0
	}
	return n
}
