// Package tftpl renders the OpenTofu `templatefile` subset the on-box reconcile
// scripts use, so an operator can produce byte-identical output to the OpenTofu
// module from the same source file (ADR 0052, MIT-1637).
//
// # Why this exists at all
//
// `user_data` runs once per instance, so a config fix baked into it can never
// reach an already-provisioned box. T-564 moved the Caddy edge config into an
// SSM document reconciled on every apply, and MIT-1272 did the same for
// `/opt/app/app.env`. Both twins were missing that entirely — they are on the
// model those two incidents replaced.
//
// Closing the gap means all three engines need the SAME two scripts. The
// CloudFormation route cannot pass them as parameters (a String parameter value
// caps at 4096 bytes and `app_env.sh` is 4903), so each operator substitutes the
// placeholders itself and injects the result. That substitution has to match
// OpenTofu's `templatefile` exactly, or the engines silently produce different
// files from one source — which is the divergence the whole exercise is
// removing.
//
// So the semantics live HERE, once, with tests, rather than being re-derived in
// each operator.
//
// # The subset, and why the escape matters most
//
//	${name}   -> the value of name
//	$${name}  -> a LITERAL ${name}
//
// The second is not a footnote. `edge_config.sh.tftpl` writes a Caddyfile
// containing `default_sni $${PUBLIC_IP}` and `https://$${PUBLIC_IP}` — those
// must survive into the script as shell expansions the BOX resolves at run time.
// An operator that treated `$${PUBLIC_IP}` as an unknown interpolation would
// either fail or, worse, blank it — producing a Caddyfile that binds the wrong
// host and a box that answers nothing on :443. That is the #526 symptom this
// script was written to fix, reintroduced by the tool meant to ship it.
//
// # The one directive, and why it is implemented rather than avoided
//
//	%{ if NAME != "" } ... %{ endif }
//
// `edge_config.sh.tftpl` uses exactly this, once, to include a Caddy site block
// only when the consumer set a domain. It was tempting to treat directives as
// out of scope and restructure the script instead — but the script is the
// CANONICAL one that has run in production, and rewriting it to suit a renderer
// would change what the OpenTofu engine does to every existing box. The
// renderer adapts to the script, not the other way round.
//
// The semantics match OpenTofu's: the directive MARKERS are removed and the
// body between them — including the newline that follows the opening marker —
// is kept or dropped whole.
//
// Deliberately NOT supported: `%{ for }`, `%{ else }`, trim markers (`%{~`),
// function calls, and any condition other than the `!= ""` form above. Each
// would need semantics this package cannot verify against OpenTofu, and a
// renderer that silently accepted one while mis-implementing it would emit a
// subtly wrong script — worse than refusing.
package tftpl

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// sentinel stands in for an escaped `$${` while interpolation runs, so an
// escaped placeholder can never be interpolated and an interpolated VALUE can
// never be re-scanned. It contains characters no shell script would carry.
const sentinel = "\x00tftpl-escaped-dollar\x00"

// placeholder matches ${name}. Names are the conservative identifier set the
// scripts actually use; anything else is reported rather than guessed at.
var placeholder = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ifBlock matches the one supported directive, non-greedily so two blocks in
// one template cannot be merged into one. The `(?s)` flag lets the body span
// lines, which it always does.
var ifBlock = regexp.MustCompile(`(?s)%\{ if ([A-Za-z_][A-Za-z0-9_]*) != "" \}(.*?)%\{ endif \}`)

// anyDirective finds directive syntax of any kind, so anything the supported
// form did not consume can be refused by name rather than passed through.
var anyDirective = regexp.MustCompile(`%\{`)

// Render substitutes vars into an OpenTofu template string.
//
// It is strict in both directions, because both mistakes are silent otherwise:
//
//   - a placeholder with no value is an ERROR, never an empty string. Rendering
//     `reverse_proxy ${app_name}-app:8080` with no app_name would emit
//     `reverse_proxy -app:8080` — a valid-looking Caddyfile pointing at nothing.
//   - a variable with no placeholder is an ERROR too. It means the caller and
//     the script disagree about the contract, which is exactly the drift the
//     single-source rule exists to prevent.
func Render(tmpl string, vars map[string]string) (string, error) {
	// Escapes first. Everything after this point cannot see them.
	work := strings.ReplaceAll(tmpl, "$${", sentinel)

	// Directives before interpolation: a dropped block's placeholders must not
	// be reported as missing values, and a kept block's must still resolve.
	work, conditioned, err := expandConditionals(work, vars)
	if err != nil {
		return "", err
	}

	// Anything still carrying directive syntax is a form this package does not
	// implement. Refuse by position rather than emit it as literal text.
	if loc := anyDirective.FindStringIndex(work); loc != nil {
		return "", fmt.Errorf("tftpl: unsupported directive at offset %d — only %%{ if NAME != \"\" } / %%{ endif } is implemented, "+
			"and emitting an unimplemented one unchanged would produce a subtly wrong script", loc[0])
	}

	var missing []string
	// A name a CONDITION consumed counts as used. `domain` is referenced only
	// inside the block it guards, so when the consumer set no domain the block
	// is dropped and no `${domain}` survives — reporting it as "supplied but
	// never used" would make the default, no-domain path the failing one.
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

	// Restore the escapes as literal `${`, which is what OpenTofu emits.
	return strings.ReplaceAll(out, sentinel, "${"), nil
}

// Lines renders and splits on newlines, the shape an SSM document's
// `runCommand` takes. Matches the OpenTofu module's
// `split("\n", templatefile(...))` so the on-box command is identical.
func Lines(tmpl string, vars map[string]string) ([]string, error) {
	rendered, err := Render(tmpl, vars)
	if err != nil {
		return nil, err
	}
	return strings.Split(rendered, "\n"), nil
}

// expandConditionals resolves `%{ if NAME != "" } ... %{ endif }`.
//
// The body is kept whole when NAME is a non-empty variable and dropped whole
// otherwise — matching OpenTofu, where the markers vanish and the text between
// them (including the newline after the opening marker) is what varies.
//
// A condition on an UNKNOWN variable is an error, not false. Treating it as
// false would silently drop the consumer's domain block and serve the app on a
// bare IP with no indication anything was omitted.
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
