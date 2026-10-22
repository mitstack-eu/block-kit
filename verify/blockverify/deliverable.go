package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func deliverableSuite(target string) SuiteResult {
	const name = "deliverable"

	path := filepath.Join(target, "Dockerfile")
	raw, err := os.ReadFile(path)
	if err != nil {
		return SuiteResult{name, "fail", fmt.Sprintf("%s has no Dockerfile", target)}
	}
	if strings.Contains(string(raw), "examples/operator-kit") {
		return SuiteResult{name, "fail", fmt.Sprintf(
			"%s/Dockerfile COPYs a monorepo-relative path (examples/operator-kit) — that build context does not exist for a standalone target",
			target)}
	}
	return SuiteResult{name, "pass", "own Dockerfile exists and assumes no monorepo build context"}
}
