// Package tftpl, an optional helper, renders the OpenTofu templatefile subset of ${name}, $${name} and %{ if NAME != "" }, and refuses everything else.
package tftpl

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// sentinel stands in for an escaped $${ so an escaped placeholder is never interpolated and a value is never scanned again.
const sentinel = "\x00tftpl-escaped-dollar\x00"

var placeholder = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ifBlock is non-greedy so two blocks in one template cannot merge.
var ifBlock = regexp.MustCompile(`(?s)%\{ if ([A-Za-z_][A-Za-z0-9_]*) != "" \}(.*?)%\{ endif \}`)

var anyDirective = regexp.MustCompile(`%\{`)

// Render refuses a placeholder with no value and a value with no placeholder, since either means the caller and the template disagree.
func Render(tmpl string, vars map[string]string) (string, error) {
	work := strings.ReplaceAll(tmpl, "$${", sentinel)

	work, conditioned, err := expandConditionals(work, vars)
	if err != nil {
		return "", err
	}

	if loc := anyDirective.FindStringIndex(work); loc != nil {
		return "", fmt.Errorf("tftpl: unsupported directive at offset %d — only %%{ if NAME != \"\" } / %%{ endif } is implemented, "+
			"and emitting an unimplemented one unchanged would produce a subtly wrong script", loc[0])
	}

	var missing []string
	seen := map[string]bool{}
	for name := range conditioned {
		seen[name] = true
	}
	out := placeholder.ReplaceAllStringFunc(work, func(m string) string {
		name := placeholder.FindStringSubmatch(m)[1]
		seen[name] = true
		v, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return m
		}
		return v
	})

	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("tftpl: no value for %s — a blank substitution would produce a valid-looking file that points nowhere",
			strings.Join(dedupe(missing), ", "))
	}

	var unused []string
	for name := range vars {
		if !seen[name] {
			unused = append(unused, name)
		}
	}
	if len(unused) > 0 {
		sort.Strings(unused)
		return "", fmt.Errorf("tftpl: %s supplied but never used by this template — "+
			"the caller and the script disagree about the contract",
			strings.Join(unused, ", "))
	}

	return strings.ReplaceAll(out, sentinel, "${"), nil
}

// Lines matches the OpenTofu module's split("\n", templatefile(...)).
func Lines(tmpl string, vars map[string]string) ([]string, error) {
	rendered, err := Render(tmpl, vars)
	if err != nil {
		return nil, err
	}
	return strings.Split(rendered, "\n"), nil
}

// expandConditionals runs before interpolation, so a dropped block's placeholders are not reported missing, and it refuses a condition on an unknown variable, since false would drop the block it guards.
func expandConditionals(s string, vars map[string]string) (string, map[string]bool, error) {
	var bad []string
	used := map[string]bool{}
	out := ifBlock.ReplaceAllStringFunc(s, func(m string) string {
		parts := ifBlock.FindStringSubmatch(m)
		name, body := parts[1], parts[2]
		used[name] = true
		v, ok := vars[name]
		if !ok {
			bad = append(bad, name)
			return ""
		}
		if v == "" {
			return ""
		}
		return body
	})
	if len(bad) > 0 {
		sort.Strings(bad)
		return "", nil, fmt.Errorf("tftpl: condition references %s, which was not supplied — "+
			"treating it as false would silently omit the block it guards",
			strings.Join(dedupe(bad), ", "))
	}
	return out, used, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
