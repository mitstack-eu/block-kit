// Package gitdiff runs git for the kit's commands that judge a change against its base commit, so each reads a change the same way.
package gitdiff

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// DiffFlags opens every diff the commands read, so no user setting reshapes the patch.
var DiffFlags = []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color"}

func Run(dir, stdin string, args ...string) (string, error) {
	c := exec.Command("git", append([]string{"-C", dir, "-c", "core.quotePath=false", "--literal-pathspecs"}, args...)...)
	c.Stdin = strings.NewReader(stdin)
	var errOut bytes.Buffer
	c.Stderr = &errOut
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(errOut.String()))
	}
	return string(out), nil
}

func SplitZ(s string) []string {
	if s = strings.TrimSuffix(s, "\x00"); s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// Untracked lists the files under paths that git neither tracks nor ignores.
func Untracked(dir string, paths ...string) ([]string, error) {
	out, err := Run(dir, "", append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--"}, paths...)...)
	return SplitZ(out), err
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// Hunk reads a hunk header: how many lines it removes, the line its additions start at, and how many lines it adds.
func Hunk(line string) (removed, first, added int, ok bool) {
	m := hunkHeader.FindStringSubmatch(line)
	if m == nil {
		return 0, 0, 0, false
	}
	first, _ = strconv.Atoi(m[2])
	return count(m[1]), first, count(m[3]), true
}

func count(s string) int {
	if s == "" {
		return 1
	}
	n, _ := strconv.Atoi(s)
	return n
}

// ReadFile reads a regular file, and reports false for a missing file or for anything that is not a regular file.
func ReadFile(name string) (string, bool, error) {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) || err == nil && !info.Mode().IsRegular() {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(name)
	return string(b), err == nil, err
}
