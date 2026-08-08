package ghapi

import (
	"fmt"
	"regexp"
	"strings"
)

// safeRepoRE matches exactly "owner/name" — no path traversal, no extra
// segments, no host/query/fragment smuggling.
var safeRepoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

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
