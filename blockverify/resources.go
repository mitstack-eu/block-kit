package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
)

const (
	resourcesTestNeedle       = "func TestDeclaredResources("
	resourcesCheckNeedle      = "kittest.AssertDeclaredResources("
	resourcesBlockCheckNeedle = "kittest.AssertBlock("
)

// resourcesSuite drives each resource a mitstack/v1beta manifest declares.
// The target's test files must define TestDeclaredResources and call
// kittest.AssertBlock or kittest.AssertDeclaredResources from it, and the
// suite then runs that test: it passes only when the block holds every
// guard the check it calls proves. A failure's detail is the first problem
// naming a resource. The manifest is read through contract.ParseManifest: a manifest
// that is not mitstack/v1beta declares no resources, so the suite is
// skipped, as it is when there is no manifest.yaml, which trust-plumbing
// already fails; a manifest the parser refuses fails with its reason.
func resourcesSuite(target string) SuiteResult {
	const name = "resources"

	path := filepath.Join(target, manifestFile)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return SuiteResult{name, "skipped", fmt.Sprintf(
			"%s has no %s (trust-plumbing reports that), so there are no declared resources to drive", target, manifestFile)}
	}
	m, err := readManifest(path)
	if err != nil {
		return SuiteResult{name, "fail", err.Error()}
	}
	if m.Block == nil {
		return SuiteResult{name, "skipped", fmt.Sprintf(
			"%s is not a %s manifest, so it declares no resources to drive", manifestFile, contract.ManifestAPIVersion)}
	}

	tests := concatGoTestFiles(target)
	if !strings.Contains(tests, resourcesTestNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf(
			"%s declares its resources but has no TestDeclaredResources test to drive them through kittest.AssertBlock", target)}
	}
	block := strings.Contains(tests, resourcesBlockCheckNeedle)
	if !block && !strings.Contains(tests, resourcesCheckNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf(
			"%s defines TestDeclaredResources but never calls kittest.AssertBlock or kittest.AssertDeclaredResources, so no declared resource is driven", target)}
	}

	ok, output := runGoTest(target, "TestDeclaredResources")
	if !ok {
		return SuiteResult{name, "fail", resourceProblemLine(output)}
	}
	if block {
		return SuiteResult{name, "pass", fmt.Sprintf(
			"TestDeclaredResources proves the block's %d declared resources match what carries the subscription's id, "+
				"observe the provider, name their replacements and observe absent after unsubscribe", len(m.Block.Spec.Resources))}
	}
	return SuiteResult{name, "pass", fmt.Sprintf(
		"TestDeclaredResources proves each of the %d declared resources survives ensure twice, observe, delete and the absence proof, "+
			"and every supplied resource still stands", len(m.Block.Spec.Resources))}
}

// resourceProblemLine picks the first line of go test output that names a
// resource, trimmed, falling back to detailFromGoTestOutput when none does.
func resourceProblemLine(output string) string {
	for _, l := range strings.Split(output, "\n") {
		if strings.Contains(l, `resource "`) {
			return strings.TrimSpace(l)
		}
	}
	return detailFromGoTestOutput(output)
}
