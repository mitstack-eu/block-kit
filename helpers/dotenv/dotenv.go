// Package dotenv reads the variable names a dotenv file assigns, once each in the order first assigned, reading an export line as any other, and never their values.
package dotenv

import (
	"regexp"
	"slices"
	"strings"
)

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func Names(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		name, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "export "), "=")
		if name = strings.TrimSpace(name); ok && nameRe.MatchString(name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}
