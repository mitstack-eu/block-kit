package main

import (
	"fmt"
	"strings"
)

const secretLeakNeedle = "func TestNoSecretLeak"
const secretLeakLogsNeedle = "func TestNoSecretLeak_LogsEndpoint"

// secretSafetySuite reimplements
// scripts/check-operator-secret-leak-tests.sh against target alone, plus —
// unlike that script, which only checks presence — actually RUNS the
// test(s), consistent with the "it RUNS the tests; it never merely detects
// them" rule scripts/block-verify.sh documents.
func secretSafetySuite(target string) SuiteResult {
	const name = "secret-safety"

	content := concatGoTestFiles(target)
	if !strings.Contains(content, secretLeakNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf("%s is missing a %s... test", target, secretLeakNeedle)}
	}

	if anyGoFileContains(target, `"/logs"`) && !strings.Contains(content, secretLeakLogsNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf("%s serves /logs but is missing a %s... test", target, secretLeakLogsNeedle)}
	}

	ok, output := runGoTest(target, "TestNoSecretLeak")
	if !ok {
		return SuiteResult{name, "fail", detailFromGoTestOutput(output)}
	}
	return SuiteResult{name, "pass", "TestNoSecretLeak (+ the /logs variant where /logs is served) passes"}
}
