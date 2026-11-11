package main

// A capability operator that answers verify-connection reaches its vendor through the kit's guarded client, and keeps the entered credential out of every answer and log.

import (
	"os"
	"testing"
)

const capabilityOperatorDir = "testdata/capability-verify-connection"

func capabilityOperator(opts ...option) dirFunc {
	return fixture(func(t *testing.T) map[string]string {
		return map[string]string{"go.mod": kitGoMod(t), "go.sum": kitGoSum(t)}
	}, append([]option{withDir(capabilityOperatorDir)}, opts...)...)
}

func operatorSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(capabilityOperatorDir + "/operator.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func operatorTestSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(capabilityOperatorDir + "/operator_test.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func editOperator(t *testing.T, pairs ...string) option {
	src := operatorSource(t)
	for i := 0; i+1 < len(pairs); i += 2 {
		src = replaced(t, src, pairs[i], pairs[i+1])
	}
	return with("operator.go", src)
}

func editOperatorTest(t *testing.T, pairs ...string) option {
	src := operatorTestSource(t)
	for i := 0; i+1 < len(pairs); i += 2 {
		src = replaced(t, src, pairs[i], pairs[i+1])
	}
	return with("operator_test.go", src)
}

func TestGuardedClientSuite(t *testing.T) {
	guardedNewClient := `func newClient(hosts []string) *http.Client {
	c, err := guardedhttp.New(guardedhttp.Config{Hosts: hosts})
	if err != nil {
		log.Fatal(err)
	}
	return c
}`
	followsRedirects := `func newClient(hosts []string) *http.Client {
	c, err := guardedhttp.New(guardedhttp.Config{Hosts: hosts})
	if err != nil {
		log.Fatal(err)
	}
	c.CheckRedirect = nil
	return c
}`
	runSuites(t, []string{"guarded-client"}, []suiteCase{
		{name: "an operator built on the kit's client passes, naming guardedhttp", dir: capabilityOperator(),
			status: "pass", detail: []string{"guardedhttp", "TestGuardedClient passes"}},
		{name: "an operator whose client follows a redirect and dials 127.0.0.1 fails, naming the raw client",
			dir: capabilityOperator(withDir("testdata/capability-unguarded-client")), status: "fail", detail: []string{"operator.go", "http.Client", "guardedhttp"}},
		{name: "an operator that calls the vendor with http.DefaultClient fails, naming it",
			dir: capabilityOperator(editOperator(t, "v.client.Do(req)", "http.DefaultClient.Do(req)")), status: "fail", detail: []string{"http.DefaultClient"}},
		{name: "an operator that dials with net.Dial fails, naming it",
			dir: capabilityOperator(withSource("dial.go", "dials-loopback.go")), status: "fail", detail: []string{"dial.go", "net.Dial"}},
		{name: "an operator that never imports guardedhttp fails, naming the import",
			dir:    capabilityOperator(editOperator(t, "\t\"github.com/mitstack-eu/block-kit/helpers/guardedhttp\"\n", "", guardedNewClient, "func newClient([]string) *http.Client { return nil }")),
			status: "fail", detail: []string{"helpers/guardedhttp"}},
		{name: "an operator whose guarded client is made to follow redirects fails in TestGuardedClient",
			dir: capabilityOperator(editOperator(t, guardedNewClient, followsRedirects)), status: "fail", detail: []string{"FAIL"}},
		{name: "an operator with no TestGuardedClient fails, naming the test",
			dir: capabilityOperator(editOperatorTest(t, "func TestGuardedClient(", "func TestClientIsGuarded(")), status: "fail", detail: []string{"TestGuardedClient"}},
		{name: "a block that lists no verify-connection is skipped, saying why", dir: passingBlock(),
			status: "skipped", detail: []string{"does not list verify-connection"}},
	})
}

func TestSecretSafetySuite_ForVerifyConnection(t *testing.T) {
	const leak = "log.Printf(\"verify-connection for org %s\", c.OrgID)"
	runSuites(t, []string{"secret-safety"}, []suiteCase{
		{name: "an operator that keeps the entered credential out of its answers and logs passes", dir: capabilityOperator(),
			status: "pass", detail: []string{"TestNoSecretLeak_VerifyConnection"}},
		{name: "an operator that logs the entered credential during verify-connection fails",
			dir:    capabilityOperator(editOperator(t, leak, "log.Printf(\"verify-connection for org %s with key %s\", c.OrgID, c.Credential[\"private_key\"])")),
			status: "fail", detail: []string{"FAIL"}},
		{name: "an operator that writes the entered credential to its /logs buffer fails",
			dir:    capabilityOperator(editOperator(t, "\"checked a credential for org \" + c.OrgID", "\"checked key \" + c.Credential[\"private_key\"]")),
			status: "fail", detail: []string{"FAIL"}},
		{name: "an operator with no TestNoSecretLeak_VerifyConnection fails, naming the test",
			dir:    capabilityOperator(editOperatorTest(t, "func TestNoSecretLeak_VerifyConnection(", "func TestNoSecretLeak(")),
			status: "fail", detail: []string{"TestNoSecretLeak_VerifyConnection"}},
		{name: "an operator whose leak test never sends a verify-connection fails, saying so",
			dir:    capabilityOperator(editOperatorTest(t, "\"operation\": contract.OperationVerifyConnection", "\"operation\": contract.OperationIssue", "Name: \"verify-connection\"", "Name: \"first\"")),
			status: "fail", detail: []string{"never sends a verify-connection request"}},
	})
}
