package handler

import (
	"errors"
	"fmt"
	"strings"
)

// errNilResource is returned by New when Config.Resource is nil.
var errNilResource = errors.New("handler: Config.Resource is required")

// knownHooks is the closed set of hook points New will ever accept a
// registration for.
var knownHooks = []string{HookBeforeEnsure, HookAfterEnsure, HookBeforeDelete, HookAfterDelete}

// validateHooks refuses a Config that would let a block override an
// invariant through Hooks: a registration under a name that is not one of
// the four HookXxx constants, a registration for a hook the manifest does
// not declare, or a non-empty Hooks with no Manifest to check it against.
func validateHooks(cfg Config) error {
	if len(cfg.Hooks) == 0 {
		return nil
	}
	if len(cfg.Manifest) == 0 {
		return errors.New("handler: Config.Hooks is set but Config.Manifest is empty — a hook can only be registered if the manifest declares it")
	}

	declared := manifestHooks(cfg.Manifest)
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
// spec.lifecycle.hooks, in the nested block-sequence shape mitstack
// manifests use:
//
//	spec:
//	  lifecycle:
//	    hooks:
//	      - beforeEnsure
//	      - afterDelete
//
// The kit takes no YAML dependency, so this reads the manifest as plain
// text, exactly as kittest/absence.go reads absenceProbe: it finds the line
// whose trimmed text is "hooks:" and collects the "- name" items indented
// under it. "hooks: []" and a manifest with no hooks: key both declare no
// hook.
func manifestHooks(manifest []byte) []string {
	lines := strings.Split(string(manifest), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "hooks: []" {
			return nil
		}
		if trimmed != "hooks:" {
			continue
		}

		indent := leadingSpaces(line)
		var hooks []string
		for _, next := range lines[i+1:] {
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
		return hooks
	}
	return nil
}

// leadingSpaces counts the spaces a line is indented by.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}
