package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type gitRepo struct{ dir string }

type change struct {
	base, head string
	added      map[int]bool
}

var hunkRE = regexp.MustCompile(`^@@ -\S+ \+(\d+)(?:,(\d+))? @@`)

var diffFlags = []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color"}

func (g gitRepo) cmd(stdin string, args ...string) (string, error) {
	c := exec.Command("git", append([]string{"-C", g.dir, "-c", "core.quotePath=false", "--literal-pathspecs"}, args...)...)
	c.Stdin = strings.NewReader(stdin)
	var errOut bytes.Buffer
	c.Stderr = &errOut
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(errOut.String()))
	}
	return string(out), nil
}

func splitZ(s string) []string {
	if s = strings.TrimSuffix(s, "\x00"); s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

func (g gitRepo) resolve(rev string) (sha, short string, err error) {
	prefix, err := g.cmd("", "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(prefix) != "" {
		return "", "", errors.New("-repo must name the repository root")
	}
	if sha, err = g.cmd("", "rev-parse", "--verify", "--quiet", rev+"^{commit}"); err != nil {
		return "", "", fmt.Errorf("-base %q is not a commit", rev)
	}
	sha = strings.TrimSpace(sha)
	short, err = g.cmd("", "rev-parse", "--short", sha)
	return sha, strings.TrimSpace(short), err
}

func (g gitRepo) changes(base string, renames bool, paths []string) ([]change, error) {
	rename := "--no-renames"
	if renames {
		rename = "-M"
	}
	status, err := g.cmd("", slices.Concat(diffFlags, []string{rename, "-z", "--name-status", base, "--"}, paths)...)
	if err != nil {
		return nil, err
	}
	var out []change
	for f := splitZ(status); len(f) >= 2; {
		switch f[0][0] {
		case 'R', 'C':
			out, f = append(out, change{base: f[1], head: f[2]}), f[3:]
		case 'A':
			out, f = append(out, change{head: f[1]}), f[2:]
		case 'D':
			out, f = append(out, change{base: f[1]}), f[2:]
		default:
			out, f = append(out, change{base: f[1], head: f[1]}), f[2:]
		}
	}
	untracked, err := g.cmd("", append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--"}, paths...)...)
	for _, p := range splitZ(untracked) {
		out = append(out, change{head: p})
	}
	return out, err
}

func (g gitRepo) addedLines(base string, c change) (map[int]bool, error) {
	patch, err := g.cmd("", slices.Concat(diffFlags, []string{"-M", "-U0", base, "--", c.base, c.head})...)
	added := map[int]bool{}
	for _, ln := range strings.Split(patch, "\n") {
		m := hunkRE.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		first, _ := strconv.Atoi(m[1])
		count := 1
		if m[2] != "" {
			count, _ = strconv.Atoi(m[2])
		}
		for l := first; l < first+count; l++ {
			added[l] = true
		}
	}
	return added, err
}

func (g gitRepo) tree(rev string) (map[string]string, error) {
	out, err := g.cmd("", "ls-tree", "-r", "-z", "--full-tree", rev)
	blobs := map[string]string{}
	for _, entry := range splitZ(out) {
		meta, p, _ := strings.Cut(entry, "\t")
		if f := strings.Fields(meta); len(f) == 3 && (f[0] == "100644" || f[0] == "100755") && judgedPath(p) {
			blobs[p] = f[2]
		}
	}
	return blobs, err
}

func (g gitRepo) read(tree map[string]string, keep func(string) bool) (map[string]string, error) {
	var paths, ids []string
	for p, id := range tree {
		if keep(p) {
			paths, ids = append(paths, p), append(ids, id)
		}
	}
	src := map[string]string{}
	if len(ids) == 0 {
		return src, nil
	}
	out, err := g.cmd(strings.Join(ids, "\n")+"\n", "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		header, rest, _ := strings.Cut(out, "\n")
		f := strings.Fields(header)
		size := -1
		if len(f) == 3 {
			size, _ = strconv.Atoi(f[2])
		}
		if size < 0 || len(rest) <= size {
			return nil, fmt.Errorf("git cat-file: unexpected reply %q for %s", header, p)
		}
		src[p], out = rest[:size], rest[size+1:]
	}
	return src, nil
}

func (g gitRepo) headFiles(keep func(string) bool) (map[string]string, error) {
	out, err := g.cmd("", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	src := map[string]string{}
	for _, p := range splitZ(out) {
		if !judgedPath(p) || !keep(p) {
			continue
		}
		s, ok, err := readFile(filepath.Join(g.dir, filepath.FromSlash(p)))
		if err != nil {
			return nil, err
		}
		if ok {
			src[p] = s
		}
	}
	return src, nil
}

func readFile(name string) (string, bool, error) {
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
