package handler

import (
	"errors"
	"fmt"
	"strings"
)

// errNilResource is returned by New when Config.Resource is nil.
var errNilResource = errors.New("handler: Config.Resource is required")

// errEmptyJWKSURL is returned by New when Config.JWKSURL is empty.
// webhook.AuthenticateHook rejects every request on an empty JWKSURL before
// it ever consults Config.PublicKey, so PublicKey is never a substitute for
// it.
var errEmptyJWKSURL = errors.New("handler: Config.JWKSURL is required")

// knownHooks is the closed set of hook points New will ever accept a
// registration for.
var knownHooks = []string{HookBeforeEnsure, HookAfterEnsure, HookBeforeDelete, HookAfterDelete}

// validateHooks refuses a Config that would let a block override an
// invariant through Hooks: a registration under a name that is not one of
// the four HookXxx constants, a registration for a hook the manifest does
// not declare, a non-empty Hooks with no Manifest to check it against, or a
// manifest whose spec.lifecycle.hooks value fits no form manifestHooks can
// read. The manifest is parsed whenever it is non-empty, even with no Hooks
// registered at all, since a declaration nobody can read must be refused
// rather than silently treated as declaring nothing.
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
	if len(cfg.Hooks) == 0 {
		return nil
	}

	declaredSet := make(map[string]bool, len(declared))
	for _, h := range declared {
		declaredSet[h] = true
	}
	knownSet := make(map[string]bool, len(knownHooks))
	for _, h := range knownHooks {
		knownSet[h] = true
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
// which — like a manifest with no hooks: key at all — declares no hook. A
// hooks: value that fits none of those forms makes manifestHooks return an
// error instead of silently declaring nothing, since a declaration nobody
// can read must not be mistaken for an empty one.
func manifestHooks(manifest []byte) ([]string, error) {
	lines := strings.Split(string(manifest), "\n")

	specLine := -1
	for i, line := range lines {
		if leadingSpaces(line) == 0 && strings.TrimSpace(line) == "spec:" {
			specLine = i
			break
		}
	}
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

// findChild scans forward from just after the line at parentLine (itself
// indented by parentIndent) for a direct child of that line: the first
// line indented more than parentIndent fixes the child indent level, and
// only a line at exactly that level counts, since anything indented deeper
// belongs to that child's own nested block, not to the parent directly.
// The scan stops as soon as indentation drops back to parentIndent or
// below, since that ends the parent's block. It returns the index of the
// first direct-child line whose trimmed text equals name or, when
// prefixOK is true, starts with name — or -1 when there is none.
func findChild(lines []string, parentLine, parentIndent int, name string, prefixOK bool) int {
	childIndent := -1
	for i := parentLine + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
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
// manifest when it fits none of them.
func parseHooksValue(lines []string, hooksLine int) ([]string, error) {
	line := lines[hooksLine]
	trimmed := strings.TrimSpace(line)
	indent := leadingSpaces(line)

	if trimmed == "hooks:" {
		var hooks []string
		for _, next := range lines[hooksLine+1:] {
			if strings.TrimSpace(next) == "" {
				continue
			}
			if leadingSpaces(next) <= indent {
				break
			}
			item := strings.TrimSpace(next)
			if !strings.HasPrefix(item, "-") {
				break
			}
			hooks = append(hooks, strings.TrimSpace(strings.TrimPrefix(item, "-")))
		}
		return hooks, nil
	}

	if rest, ok := strings.CutPrefix(trimmed, "hooks:"); ok {
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "[") && strings.HasSuffix(rest, "]") {
			inner := strings.TrimSpace(rest[1 : len(rest)-1])
			if inner == "" {
				return nil, nil
			}
			items := strings.Split(inner, ",")
			hooks := make([]string, 0, len(items))
			for _, it := range items {
				hooks = append(hooks, strings.TrimSpace(it))
			}
			return hooks, nil
		}
	}

	return nil, fmt.Errorf("handler: manifest's spec.lifecycle.hooks is not a form New can read "+
		"(want %q, a flow list like %q, or an indented \"- name\" sequence): %q", "hooks: []", "hooks: [a, b]", trimmed)
}

// leadingSpaces counts the spaces a line is indented by.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}
