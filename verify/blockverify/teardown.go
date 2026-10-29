package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
)

const teardownNeedle = "func TestUnsubscribeDeletes"

const teardownDeletesDetail = "TestUnsubscribeDeletes* proves unsubscribe deletes what subscribe created"

// teardownSuite runs TestUnsubscribeDeletes* as well as checking it exists, so a broken teardown test fails the gate, and for a block declaring a file-landing type it runs TestDeclaredResources, whose kittest.AssertBlock reads main after unsubscribe.
func teardownSuite(target string) SuiteResult {
	const name = "teardown"

	if detail, ok := requireKitV1Contract(target); !ok {
		return SuiteResult{name, "fail", detail}
	}

	if !strings.Contains(concatGoTestFiles(target), teardownNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf("%s is missing a %s... test", target, teardownNeedle)}
	}

	if detail, ok := refuseKitFake(target, "TestUnsubscribeDeletes"); !ok {
		return SuiteResult{name, "fail", detail}
	}

	ok, output := runGoTest(target, "TestUnsubscribeDeletes")
	if !ok {
		return SuiteResult{name, "fail", detailFromGoTestOutput(output)}
	}

	detail := fmt.Sprintf("%s rule: %s; the kit's handler asks the provider again after a delete", kitV1Contract, teardownDeletesDetail)
	status, detail := readsMainAfterUnsubscribe(target, detail)
	return SuiteResult{name, status, detail}
}

// readsMainAfterUnsubscribe runs TestDeclaredResources for a block declaring a file-landing type and passes any other block with detail.
func readsMainAfterUnsubscribe(target, detail string) (status, out string) {
	landed, err := landingTypes(target)
	if err != nil {
		return "fail", err.Error()
	}
	if len(landed) == 0 {
		return "pass", detail
	}
	if !strings.Contains(concatGoTestFiles(target), resourcesTestNeedle) {
		return "fail", fmt.Sprintf(
			"%s declares file-landing type %s but has no TestDeclaredResources test, so no kittest.AssertBlock reads main after unsubscribe", target, landed[0])
	}
	if ok, output := runGoTest(target, "TestDeclaredResources", "-v"); !ok {
		return "fail", unsubscribeProblemLine(output)
	}
	return "pass", fmt.Sprintf("%s; TestDeclaredResources read main after unsubscribe for file-landing type %s", detail, strings.Join(landed, ", "))
}

// landingTypes lists each declared resource type that lands files, none for a block with no manifest.
func landingTypes(target string) ([]string, error) {
	m, err := readManifest(filepath.Join(target, manifestFile))
	if err != nil || m.Block == nil {
		return nil, err
	}
	var out []string
	for _, r := range m.Block.Spec.Resources {
		for _, typ := range r.Types {
			if landing.LandsFiles(typ) {
				out = append(out, typ)
			}
		}
	}
	return out, nil
}

func unsubscribeProblemLine(output string) string {
	for _, l := range strings.Split(output, "\n") {
		if strings.Contains(l, "after unsubscribe") {
			return strings.TrimSpace(l)
		}
	}
	return resourceProblemLine(output)
}
