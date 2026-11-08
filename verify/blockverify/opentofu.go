package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// goOperatorSuiteNames are the suites after trust-plumbing that goBlockSuites runs, in run order.
var goOperatorSuiteNames = []string{"contract", "secret-safety", "guarded-client", "teardown", "resources"}

// opentofuOnlySuites answers trust-plumbing and each Go operator suite for a block whose image serves an OpenTofu module through the opentofu-operator binary.
func opentofuOnlySuites(target string) []SuiteResult {
	suites := []SuiteResult{opentofuTrustPlumbingSuite(target)}
	for _, name := range goOperatorSuiteNames {
		suites = append(suites, SuiteResult{name, "skipped",
			"--opentofu-only judges a block with no Go code of its own, so this suite, which judges a Go operator, has nothing to run; trust-plumbing fails such a block that holds Go code"})
	}
	return suites
}

// opentofuTrustPlumbingSuite fails a tree holding Go code, so --opentofu-only never excuses a Go block from the suites that judge it.
func opentofuTrustPlumbingSuite(target string) SuiteResult {
	const name = "trust-plumbing"

	if _, err := os.Stat(filepath.Join(target, manifestFile)); err != nil {
		return SuiteResult{name, "fail", fmt.Sprintf("%s is missing manifest.yaml", target)}
	}
	if detail, ok := requireKitV1Contract(target); !ok {
		return SuiteResult{name, "fail", detail}
	}
	goCode, err := goCodeIn(target)
	if err != nil {
		return SuiteResult{name, "fail", fmt.Sprintf("could not scan %s for Go code, so whether --opentofu-only can judge it is unknown: %v", target, err)}
	}
	if len(goCode) > 0 {
		return SuiteResult{name, "fail", fmt.Sprintf(
			"%s holds Go code in %s, which --opentofu-only does not judge; run blockverify without --opentofu-only to judge a block with an operator of its own",
			target, namedFiles(goCode))}
	}
	module, err := rootModuleFile(target)
	if err != nil {
		return SuiteResult{name, "fail", fmt.Sprintf("could not read %s, so its OpenTofu module is unknown: %v", target, err)}
	}
	if module == "" {
		return SuiteResult{name, "fail", fmt.Sprintf("%s holds no .tf or .tofu file at its root, so it has no OpenTofu module for --opentofu-only to judge", target)}
	}
	return SuiteResult{name, "pass", fmt.Sprintf(
		"%s rule: an OpenTofu module, %s, with no Go code of its own, manifest.yaml present", kitV1Contract, module)}
}

// goCodeIn lists, relative to target, each go.mod and .go file outside .git.
func goCodeIn(target string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == ".git":
			return filepath.SkipDir
		case d.IsDir():
			return nil
		case d.Name() == "go.mod" || strings.HasSuffix(d.Name(), ".go"):
			rel, err := filepath.Rel(target, path)
			if err != nil {
				return err
			}
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	return found, err
}

// rootModuleFile names the first .tf or .tofu file at target's root, where the root module lives, or "" when there is none.
func rootModuleFile(target string) (string, error) {
	entries, err := os.ReadDir(target)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); !e.IsDir() && (ext == ".tf" || ext == ".tofu") {
			return e.Name(), nil
		}
	}
	return "", nil
}
