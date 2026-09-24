package main

import (
	"fmt"
	"strings"
)

// contractRequiredTests are the tests every block must define — see
// scripts/check-operator-contract-tests.sh's required_tests, which this list
// mirrors exactly. A block author outside this repo runs block-verify instead
// of the shell gate, so a stale entry here means a real operator passes
// verification with a requirement nobody checked.
var contractRequiredTests = []string{
	"TestContract_DecodesEveryValidGolden",
	"TestContract_RejectsMalformedGoldens",
	"TestContract_EmitsConformingResponse",
}

// contractSuite reimplements scripts/check-operator-contract-tests.sh
// against target alone: an explicit PRESENCE check for every required
// TestContract_* functions BEFORE running `go test -run TestContract_
// ./...` — zero tests matching that pattern exits 0 (a vacuous pass), which
// is exactly the gap the presence check closes.
//
// On kit/v1 the kit owns the webhook-contract goldens, so the block defines
// no TestContract_* function and none is run. The handler-import check takes
// the place of the presence check, so the suite still reports a real verdict
// rather than the vacuous pass an unguarded go test run would give.
func contractSuite(target string) SuiteResult {
	const name = "contract"

	contract, detail, ok := contractRule(target)
	if !ok {
		return SuiteResult{name, "fail", detail}
	}
	if contract == kitV1Contract {
		file, ok := checkKitHandlerImport(target)
		if !ok {
			return SuiteResult{name, "fail", file}
		}
		return SuiteResult{name, "pass", fmt.Sprintf(
			"%s rule: the kit owns the webhook-contract goldens, so %s are waived; %s imports %s",
			kitV1Contract, strings.Join(contractRequiredTests, ", "), file, kitHandlerPackage)}
	}

	missing := missingTestFuncs(target, contractRequiredTests)
	if len(missing) > 0 {
		return SuiteResult{name, "fail", fmt.Sprintf("%s is missing %s", target, strings.Join(missing, ", "))}
	}

	ok, output := runGoTest(target, "TestContract_")
	if !ok {
		return SuiteResult{name, "fail", detailFromGoTestOutput(output)}
	}
	return SuiteResult{name, "pass", "TestContract_* accepts every valid golden and rejects the malformed ones"}
}

// missingTestFuncs returns the subset of want whose "func <name>(" is not
// present anywhere in target's *_test.go files.
func missingTestFuncs(target string, want []string) []string {
	content := concatGoTestFiles(target)
	var missing []string
	for _, fn := range want {
		if !strings.Contains(content, "func "+fn+"(") {
			missing = append(missing, fn)
		}
	}
	return missing
}
