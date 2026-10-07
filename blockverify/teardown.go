package main

import (
	"fmt"
	"strings"
)

const teardownNeedle = "func TestUnsubscribeDeletes"

// teardownSuite runs TestUnsubscribeDeletes* as well as checking it exists, so a broken teardown test fails the gate.
func teardownSuite(target string) SuiteResult {
	const name = "teardown"

	if !strings.Contains(concatGoTestFiles(target), teardownNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf("%s is missing a %s... test", target, teardownNeedle)}
	}

	ok, output := runGoTest(target, "TestUnsubscribeDeletes")
	if !ok {
		return SuiteResult{name, "fail", detailFromGoTestOutput(output)}
	}
	return SuiteResult{name, "pass", "TestUnsubscribeDeletes* proves unsubscribe deletes what subscribe created"}
}
