package main

import (
	"os/exec"
	"strings"
)

// runGoTest runs `go test <flags> -run <pattern> ./...` with dir as the
// working directory, returning whether it passed and its combined output.
//
// The subprocess inherits the calling process's environment UNCHANGED — no
// PATH re-pinning here — so it resolves "go" to whatever toolchain the
// caller (CI, or a human invoking blockverify) already has on PATH. That is
// the MIT-1586 harness lesson: scripts/block-verify_test.sh's own harness
// sets PATH before invoking the tool under test, rather than the tool doing
// it itself; blockverify follows the same discipline.
func runGoTest(dir, pattern string, flags ...string) (ok bool, output string) {
	args := append(append([]string{"test"}, flags...), "-run", pattern, "./...")
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

// detailFromGoTestOutput extracts a single representative line from `go
// test` output for a suite's Detail — the first FAIL/FAILED line, falling
// back to the last line, mirroring scripts/block-verify.sh's run_suite.
func detailFromGoTestOutput(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
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
