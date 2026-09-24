package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// kitV1Contract is the lifecycle contract on which the kit owns the trust
// boundary, the /hook route and the webhook-contract goldens.
const kitV1Contract = "kit/v1"

// kitHandlerPackage is the import that binds a kit/v1 block to the kit.
const kitHandlerPackage = "github.com/mitstack-eu/operator-kit/handler"

// manifestFile is the block manifest every target carries at its root.
const manifestFile = "manifest.yaml"

// contractRule reads the lifecycle contract target declares and returns the
// one blockverify judges it by: "" for the legacy rule, or kitV1Contract.
// When no rule applies it returns ok false and a failing detail: a manifest
// that cannot be read, or a declared value blockverify has no rule for.
// Neither falls through to the legacy rule, which would judge the block by a
// contract it never declared.
func contractRule(target string) (contract, detail string, ok bool) {
	c, err := declaredContract(target)
	if err != nil {
		return "", err.Error(), false
	}
	switch c {
	case "", kitV1Contract:
		return c, "", true
	}
	return "", fmt.Sprintf(
		"%s declares lifecycle contract %q, which blockverify has no rule for (it knows %s, and no declared contract for the legacy rule)",
		filepath.Join(target, manifestFile), c, kitV1Contract), false
}

// declaredContract returns spec.lifecycle.contract from target's manifest,
// or "" when the manifest is absent or declares no lifecycle contract. A
// manifest that is present but cannot be opened (a directory, a dangling
// symlink) or walked (a structure the reader cannot follow) is an error,
// because a read that could not run is never an absence.
func declaredContract(target string) (string, error) {
	path := filepath.Join(target, manifestFile)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s is present but could not be read, so the lifecycle contract that judges %s is unknown: %v", path, target, err)
	}
	value, walkable := parseLifecycleContract(string(raw))
	if !walkable {
		return "", fmt.Errorf("%s holds a structure the reader cannot follow under spec or spec.lifecycle, so the lifecycle contract that judges %s is unknown", path, target)
	}
	return value, nil
}

var (
	yamlKeyRe      = regexp.MustCompile(`^[A-Za-z0-9_.-]+[ \t]*:`)
	yamlAnchorOnly = regexp.MustCompile(`^&[A-Za-z0-9_-]+$`)
)

// parseLifecycleContract walks a manifest by indentation and returns the
// value of the key nested at spec.lifecycle.contract. It follows block
// mappings only: a contract: key inside a block scalar, under params or
// anywhere else is not the lifecycle contract. walkable is false when spec
// or spec.lifecycle is not a block mapping (a flow mapping, an alias), or a
// direct child of spec.lifecycle is neither a parseable key nor a list item,
// since the contract could hide there under a spelling the reader cannot see.
func parseLifecycleContract(content string) (value string, walkable bool) {
	type frame struct {
		indent int
		key    string
	}
	var stack []frame
	scalar, life, child := -1, -1, -1

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		rest := strings.TrimLeft(line, " \t")
		if rest == "" {
			continue
		}
		ind := len(line) - len(rest)
		if scalar >= 0 {
			if ind > scalar {
				continue
			}
			scalar = -1
		}
		if strings.HasPrefix(rest, "#") {
			continue
		}
		if life >= 0 && ind <= life {
			life, child = -1, -1
		}
		isKey := yamlKeyRe.MatchString(rest)
		if life >= 0 {
			if child < 0 {
				child = ind
			}
			isItem := strings.HasPrefix(rest, "- ") || strings.HasPrefix(rest, "-\t")
			if ind == child && !isKey && !isItem {
				return "", false
			}
		}
		if !isKey {
			continue
		}

		colon := strings.Index(rest, ":")
		key := strings.TrimRight(rest[:colon], " \t")
		val := strings.TrimLeft(rest[colon+1:], " \t")
		for len(stack) > 0 && stack[len(stack)-1].indent >= ind {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, frame{ind, key})
		if strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">") {
			scalar = ind
		}

		blockMapping := val == "" || strings.HasPrefix(val, "#") || yamlAnchorOnly.MatchString(val)
		depth := len(stack)
		underSpec := stack[0].key == "spec"
		if depth == 1 && underSpec && !blockMapping {
			return "", false
		}
		if depth == 2 && underSpec && stack[1].key == "lifecycle" {
			if !blockMapping {
				return "", false
			}
			life, child = ind, -1
		}
		if depth == 3 && underSpec && stack[1].key == "lifecycle" && stack[2].key == "contract" {
			return scalarValue(val), true
		}
	}
	return "", true
}

// scalarValue strips an inline comment, surrounding blanks and one pair of
// quotes from a plain YAML scalar.
func scalarValue(val string) string {
	if strings.HasPrefix(val, "#") {
		return ""
	}
	if hash := strings.Index(val, " #"); hash >= 0 {
		val = val[:hash]
	}
	val = strings.TrimRight(val, " \t")
	if val != "" && strings.ContainsRune(`"'`, rune(val[0])) {
		val = val[1:]
	}
	if val != "" && strings.ContainsRune(`"'`, rune(val[len(val)-1])) {
		val = val[:len(val)-1]
	}
	return val
}

// checkKitHandlerImport requires some non-test .go file under target to
// import kitHandlerPackage. An import inside a _test.go file binds a test
// rather than what the block serves, so it does not count. A scan that could
// not run fails rather than reading as an absent import.
func checkKitHandlerImport(target string) (detail string, ok bool) {
	needle := `"` + kitHandlerPackage + `"`
	var found string
	err := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), needle) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return fmt.Sprintf("%s rule: could not scan %s for a non-test .go file importing %s: %v",
			kitV1Contract, target, kitHandlerPackage, err), false
	}
	if found == "" {
		return fmt.Sprintf("%s rule: no non-test .go file under %s imports %s, which is what binds a %s block to the kit",
			kitV1Contract, target, kitHandlerPackage, kitV1Contract), false
	}
	rel, err := filepath.Rel(target, found)
	if err != nil {
		rel = found
	}
	return rel, true
}
