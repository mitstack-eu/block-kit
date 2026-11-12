package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/verify/internal/kitbinding"
)

const (
	replacementTestNeedle  = "func TestReplacementDrill("
	replacementCheckNeedle = "kittest.AssertReplacement("
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
	units := snapshotUnits(m.Block.Spec)
	if len(units) == 0 {
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
	drilled := kitbinding.DrilledUnits(output)
	if len(drilled) == 0 {
		return SuiteResult{name, "fail", "TestReplacementDrill passed without kittest.AssertReplacement logging its stages, so the drill ran no stage, as when the test skips before it"}
	}
	var undrilled []string
	for _, unit := range units {
		if _, ok := drilled[unit]; !ok {
			undrilled = append(undrilled, fmt.Sprintf("%q", unit))
		}
	}
	if len(undrilled) > 0 {
		return SuiteResult{name, "fail", fmt.Sprintf(
			"%s hands %s over by snapshot, but TestReplacementDrill ran no drill of it, so nothing proves that state survives a replacement",
			target, strings.Join(undrilled, " and "))}
	}
	return SuiteResult{name, "pass", fmt.Sprintf("TestReplacementDrill replaces the server and restores every datum of %s; stages that ran: %s",
		quotedList(units), drilled[units[0]])}
}

// snapshotUnits lists, in manifest order, the spec.continuity entries handed over by snapshot.
func snapshotUnits(spec contract.ManifestSpec) []string {
	var units []string
	for _, u := range spec.Continuity {
		if u.Handover == contract.HandoverSnapshot {
			units = append(units, u.Name)
		}
	}
	return units
}

func quotedList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

// replacementProblemLine picks the first problem the drill logged, and falls back to the failing line of the run.
func replacementProblemLine(output string) string {
	for _, l := range strings.Split(output, "\n") {
		if testLogPrefix.MatchString(l) && !strings.Contains(l, kitbinding.DrillRanPrefix) {
			return testLogPrefix.ReplaceAllString(l, "")
		}
	}
	return detailFromGoTestOutput(output)
}
