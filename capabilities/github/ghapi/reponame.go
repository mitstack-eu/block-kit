package ghapi

import (
	"fmt"
	"regexp"
	"strings"
)

var safeRepoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// safeVariableNameRE mirrors GitHub's naming rule and keeps "/", ".." and an encoded slash out of the PATCH fallback's URL path.
var safeVariableNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SafeVariableName validates name against GitHub's Actions-variable naming rule so it cannot smuggle a path segment into a URL.
func SafeVariableName(name string) (string, error) {
	if !safeVariableNameRE.MatchString(name) {
		return "", fmt.Errorf("ghapi: invalid variable name %q", name)
	}
	return name, nil
}

// SafeRepo validates fullName as an "owner/name" reference so it cannot smuggle a host, path traversal or extra segments into a URL.
func SafeRepo(fullName string) (string, error) {
	if !safeRepoRE.MatchString(fullName) {
		return "", fmt.Errorf("ghapi: invalid repo %q", fullName)
	}
	owner, name, _ := strings.Cut(fullName, "/")
	if owner == "." || owner == ".." || name == "." || name == ".." {
		return "", fmt.Errorf("ghapi: invalid repo %q", fullName)
	}
	return fullName, nil
}
