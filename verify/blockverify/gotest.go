package main

import (
	"os/exec"
	"strings"

	"github.com/mitstack-eu/block-kit/verify/internal/kitbinding"
)

// runGoTest inherits the environment unchanged, so "go" resolves to the caller's own toolchain on PATH.
func runGoTest(dir, pattern string, flags ...string) (ok bool, output string) {
	args := append(append([]string{"test"}, flags...), "-run", pattern, "./...")
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

// detailFromGoTestOutput picks the handler's refusal of the block, else the first FAIL line, else the last line, so a suite's detail names what failed.
func detailFromGoTestOutput(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	for _, l := range lines {
		if _, refusal, found := strings.Cut(l, kitbinding.UnservableBlock); found {
			return kitbinding.UnservableBlock + refusal
		}
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "FAIL") || strings.Contains(l, "FAILED") {
			return l
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}
