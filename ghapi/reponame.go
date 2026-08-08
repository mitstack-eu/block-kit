package ghapi

import (
	"fmt"
	"regexp"
	"strings"
)

// safeRepoRE matches exactly "owner/name" — no path traversal, no extra
// segments, no host/query/fragment smuggling.
var safeRepoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// safeVariableNameRE mirrors GitHub's own Actions-variable naming rule:
// letters/digits/underscore only, must not start with a digit, must not be
// empty. It doubles as the guard against a name that smuggles "/", "..", or
// an encoded slash into the PATCH fallback's URL path.
var safeVariableNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SafeVariableName validates name against GitHub's Actions-variable naming
// rule so it can't be used to smuggle a path segment into an outbound
// GitHub API URL (mirrors SafeRepo). On success it returns name unchanged.
func SafeVariableName(name string) (string, error) {
	if !safeVariableNameRE.MatchString(name) {
		return "", fmt.Errorf("ghapi: invalid variable name %q", name)
	}
	return name, nil
}

// SafeRepo validates fullName as an "owner/name" GitHub repo reference so it
// can't be used to smuggle a host, path traversal, or extra path segments
// into an outbound GitHub API URL (mirrors urlguard.go's safeRepoSegment).
// On success it returns fullName unchanged.
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
