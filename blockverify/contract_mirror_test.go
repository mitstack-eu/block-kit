package main

// contract_mirror_test.go pins blockverify's Go list of required contract
// tests to the shell gate it reimplements.
//
// contract.go's contractRequiredTests and
// scripts/check-operator-contract-tests.sh's required_tests are two copies of
// one decision: which TestContract_* functions every operator must define.
// The shell copy runs in CI on the operators inside this repo; the Go copy is
// what an operator author OUTSIDE this repo runs, through block-verify
// standalone (the MIT-1466 audience). Nothing links them, so when MIT-1136
// added the response half to the shell array the Go mirror kept verifying two
// of three requirements and reported a pass — an external operator could ship
// an entirely unchecked response shape and be told it conformed.
//
// So this test does not restate the list. It PARSES the shell script at test
// time and asserts set equality, which means a future change to either copy
// alone fails here rather than silently widening the gap between what CI
// demands and what an external author is told.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// contractGateScriptPath is the gate contract.go reimplements, relative to the
// repository root.
const contractGateScriptPath = "scripts/check-operator-contract-tests.sh"

// findContractGateScript walks up from the test's working directory looking
// for the gate script, so the lookup survives being run from anywhere in the
// tree and does not encode how deep this package sits. It returns "" when the
// script cannot be found at all, which is the legitimate case of the kit
// module being consumed standalone, detached from this repository.
func findContractGateScript(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}

	for {
		candidate := filepath.Join(dir, contractGateScriptPath)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

var (
	requiredTestsArrayOpen  = regexp.MustCompile(`^\s*required_tests=\(`)
	requiredTestsArrayClose = regexp.MustCompile(`^\s*\)\s*$`)
	quotedTestName          = regexp.MustCompile(`"([A-Za-z0-9_]+)"`)
)

// parseShellRequiredTests reads the test names out of the gate script's
// required_tests=( ... ) array.
func parseShellRequiredTests(t *testing.T, path string) []string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var (
		names  []string
		inside bool
		found  bool
	)
	for _, line := range strings.Split(string(raw), "\n") {
		if !inside {
			if requiredTestsArrayOpen.MatchString(line) {
				inside, found = true, true
				// An entry may sit on the opening line itself.
				names = append(names, matchedTestNames(line)...)
			}
			continue
		}
		if requiredTestsArrayClose.MatchString(line) {
			inside = false
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		names = append(names, matchedTestNames(line)...)
	}

	if !found {
		t.Fatalf("%s no longer declares a required_tests=( ... ) array — this guard "+
			"cannot compare the two copies of the contract-test list, and a silent skip "+
			"here is exactly the blindness it exists to prevent", path)
	}
	if inside {
		t.Fatalf("%s's required_tests=( ... ) array is never closed by a line containing only ')'", path)
	}
	if len(names) == 0 {
		t.Fatalf("%s's required_tests array parsed as empty — either the array really is "+
			"empty (in which case the gate requires nothing) or this parser no longer "+
			"understands its shape", path)
	}
	return names
}

func matchedTestNames(line string) []string {
	var out []string
	for _, m := range quotedTestName.FindAllStringSubmatch(line, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestContractRequiredTests_MirrorTheShellGateExactly(t *testing.T) {
	script := findContractGateScript(t)
	if script == "" {
		t.Skipf("could not locate %s by walking up from the working directory — "+
			"the operator-kit module is being consumed standalone, detached from the "+
			"mitstack repository, so there is no shell gate to compare against",
			contractGateScriptPath)
	}

	shell := parseShellRequiredTests(t, script)

	gotSorted := append([]string(nil), contractRequiredTests...)
	wantSorted := append([]string(nil), shell...)
	sort.Strings(gotSorted)
	sort.Strings(wantSorted)

	if strings.Join(gotSorted, "\n") == strings.Join(wantSorted, "\n") {
		return
	}

	missing := setDifference(wantSorted, gotSorted)
	extra := setDifference(gotSorted, wantSorted)

	t.Errorf("contract.go's contractRequiredTests has drifted from %s's required_tests.\n"+
		"  shell gate requires: %v\n"+
		"  blockverify checks:  %v\n"+
		"  missing from blockverify: %v\n"+
		"  present only in blockverify: %v\n"+
		"An operator author outside this repo runs blockverify, not the shell gate, so "+
		"anything missing here is a requirement they are never told about.",
		contractGateScriptPath, wantSorted, gotSorted, missing, extra)
}

func setDifference(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, v := range b {
		inB[v] = true
	}
	var out []string
	for _, v := range a {
		if !inB[v] {
			out = append(out, v)
		}
	}
	return out
}
