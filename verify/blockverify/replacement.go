package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
)

const (
	replacementTestNeedle  = "func TestReplacementDrill("
	replacementCheckNeedle = "kittest.AssertReplacement("
	drillRanMarker         = "kittest.AssertReplacement drill ran:"
)

// testLogPrefix is the file and line go test puts before each line a test logs.
var testLogPrefix = regexp.MustCompile(`^\s*\S+_test\.go:\d+:\s*`)

func replacementSuite(target string) SuiteResult {
	const name = "replacement"

	path := filepath.Join(target, manifestFile)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return SuiteResult{name, "skipped", fmt.Sprintf(
			"%s has no %s (trust-plumbing reports that), so there is no declared state to replace", target, manifestFile)}
	}
	m, err := readManifest(path)
	if err != nil {
		return SuiteResult{name, "fail", err.Error()}
	}
	if !slices.ContainsFunc(m.Block.Spec.Continuity, func(u contract.ManifestContinuity) bool { return u.Handover == contract.HandoverSnapshot }) {
		return SuiteResult{name, "skipped", "the manifest declares no state handed over by snapshot under spec.continuity, so there is nothing to replace and restore"}
	}

	tests := concatGoTestFiles(target)
	if !strings.Contains(tests, replacementTestNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf(
			"%s hands state over by snapshot but has no TestReplacementDrill test to replace its server through kittest.AssertReplacement", target)}
	}
	if !strings.Contains(tests, replacementCheckNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf(
			"%s defines TestReplacementDrill but never calls kittest.AssertReplacement, so no replacement is driven", target)}
	}

	ok, output := runGoTest(target, "TestReplacementDrill", "-v")
	if !ok {
		return SuiteResult{name, "fail", replacementProblemLine(output)}
	}
	_, ran, logged := strings.Cut(output, drillRanMarker)
	if !logged {
		return SuiteResult{name, "fail", "TestReplacementDrill passed without kittest.AssertReplacement logging its stages, so the drill ran no stage, as when the test skips before it"}
	}
	ran, _, _ = strings.Cut(ran, "\n")
	return SuiteResult{name, "pass", fmt.Sprintf("TestReplacementDrill replaces the server and restores every datum; stages that ran: %s", strings.TrimSpace(ran))}
}

// replacementProblemLine picks the first problem the drill logged, and falls back to the failing line of the run.
func replacementProblemLine(output string) string {
	for _, l := range strings.Split(output, "\n") {
		if testLogPrefix.MatchString(l) && !strings.Contains(l, drillRanMarker) {
			return testLogPrefix.ReplaceAllString(l, "")
		}
	}
	return detailFromGoTestOutput(output)
}
