package main

import (
	"fmt"
	"strings"
)

const teardownNeedle = "func TestUnsubscribeDeletes"

// teardownSuite reimplements scripts/check-operator-teardown-tests.sh
// against target alone: presence AND a real run of TestUnsubscribeDeletes*
// (MIT-1124's teardown contract) — a broken teardown test must fail this
// gate, not merely be absent.
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
