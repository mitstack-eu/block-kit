package main

import (
	"fmt"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
)

const secretLeakNeedle = "func TestNoSecretLeak"
const secretLeakLogsNeedle = "func TestNoSecretLeak_LogsEndpoint"
const secretLeakVerifyNeedle = "func TestNoSecretLeak_VerifyConnection"

// secretSafetySuite runs the leak tests as well as checking they exist, which the shell script does not.
func secretSafetySuite(target string) SuiteResult {
	const name = "secret-safety"

	content := concatGoTestFiles(target)
	if !strings.Contains(content, secretLeakNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf("%s is missing a %s... test", target, secretLeakNeedle)}
	}

	if anyGoFileContains(target, `"/logs"`) && !strings.Contains(content, secretLeakLogsNeedle) {
		return SuiteResult{name, "fail", fmt.Sprintf("%s serves /logs but is missing a %s... test", target, secretLeakLogsNeedle)}
	}

	if declaresVerifyConnection(target) {
		if !strings.Contains(content, secretLeakVerifyNeedle) {
			return SuiteResult{name, "fail", fmt.Sprintf("%s lists verify-connection but is missing a %s... test", target, secretLeakVerifyNeedle)}
		}
		if !strings.Contains(content, contract.OperationVerifyConnection) && !strings.Contains(content, "OperationVerifyConnection") {
			return SuiteResult{name, "fail", fmt.Sprintf("%s has a %s... test that never sends a %s request", target, secretLeakVerifyNeedle, contract.OperationVerifyConnection)}
		}
	}

	if detail, ok := refuseKitFake(target, "TestNoSecretLeak"); !ok {
		return SuiteResult{name, "fail", detail}
	}

	ok, output := runGoTest(target, "TestNoSecretLeak")
	if !ok {
		return SuiteResult{name, "fail", detailFromGoTestOutput(output)}
	}
	if declaresVerifyConnection(target) {
		return SuiteResult{name, "pass", "TestNoSecretLeak and TestNoSecretLeak_VerifyConnection pass, the latter sending a verify-connection request"}
	}
	return SuiteResult{name, "pass", "TestNoSecretLeak (+ the /logs variant where /logs is served) passes"}
}
