package scripts_test

// go-2026-5932-exception.sh accepts GO-2026-5932 only in a provider binary whose Go function table names x/crypto code and no openpgp code, from a provider whose newest release is not on a fixed x/crypto, and refuses every other finding.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	plugins        = "var/cache/tofu-plugins/registry.opentofu.org/"
	fixtureTarget  = plugins + "acme/fixture/1.0.0/linux_amd64/terraform-provider-fixture_v1.0.0"
	awsTarget      = plugins + "hashicorp/aws/6.71.0/linux_amd64/terraform-provider-aws_v6.71.0_x5"
	scalewayTarget = plugins + "scaleway/scaleway/2.89.0/linux_amd64/terraform-provider-scaleway_v2.89.0"
	operatorTarget = "usr/local/bin/opentofu-operator"
	fixtureModule  = "github.com/acme/terraform-provider-fixture"
	fixedCrypto    = "v0.58.1-0.20261009181718-2f1f834330b2"
	moveFixture    = "move the block to registry.opentofu.org/acme/fixture 1.1.0"
)

type finding struct{ kind, target, id, pkg, installed, fixed string }

func advisoryIn(target string) finding {
	return finding{"vuln", target, "GO-2026-5932", "golang.org/x/crypto", "v0.58.0", strings.TrimPrefix(fixedCrypto, "v")}
}

func trivyReport(t *testing.T, findings ...finding) string {
	t.Helper()
	var results []map[string]any
	byTarget := map[string]map[string]any{}
	for _, f := range findings {
		r, ok := byTarget[f.target]
		if !ok {
			r = map[string]any{"Target": f.target, "Class": "lang-pkgs", "Type": "gobinary"}
			byTarget[f.target] = r
			results = append(results, r)
		}
		if f.kind == "secret" {
			r["Secrets"] = append(asList(r["Secrets"]), map[string]string{"RuleID": f.id})
			continue
		}
		r["Vulnerabilities"] = append(asList(r["Vulnerabilities"]),
			map[string]string{"VulnerabilityID": f.id, "PkgName": f.pkg, "InstalledVersion": f.installed, "FixedVersion": f.fixed})
	}
	raw, err := json.Marshal(map[string]any{"SchemaVersion": 2, "ArtifactName": "block:test", "Results": results})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	writeFile(t, path, string(raw))
	return path
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// fixtureBinary builds the fixture provider as module, stripped as provider releases are, at golang.org/x/crypto v0.58.0.
func fixtureBinary(t *testing.T, module, tags string) string {
	t.Helper()
	src := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum", "main.go", "blake2b.go", "sha256.go", "openpgp.go", "no_openpgp.go"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "fixture-provider", name))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(src, name), strings.Replace(string(raw), "module "+fixtureModule, "module "+module, 1))
	}
	out := filepath.Join(t.TempDir(), "terraform-provider")
	cmd := exec.Command("go", "build", "-C", src, "-tags", tags, "-trimpath", "-ldflags=-s", "-o", out, ".")
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the fixture provider %s with tags %q: %v\n%s", module, tags, err, msg)
	}
	return out
}

func imageRoot(t *testing.T, binaries map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for target, bin := range binaries {
		raw, err := os.ReadFile(bin)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, target), string(raw))
	}
	return root
}

func serve(t *testing.T, routes map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func registryVersions(versions ...string) string {
	var list []map[string]string
	for _, v := range versions {
		list = append(list, map[string]string{"version": v})
	}
	raw, _ := json.Marshal(map[string]any{"versions": list})
	return string(raw)
}

func goModRequiring(module, crypto string) string {
	mod := "module " + module + "\n\ngo 1.26\n\nrequire (\n\tgithub.com/hashicorp/terraform-plugin-go v0.29.0\n"
	if crypto != "" {
		mod += "\tgolang.org/x/crypto " + crypto + " // indirect\n"
	}
	return mod + ")\n"
}

// fixtureNewest answers that the fixture provider's newest release is newest, whose go.mod requires crypto.
func fixtureNewest(newest, crypto string) map[string]string {
	return map[string]string{
		"/v1/providers/acme/fixture/versions":                     registryVersions("1.0.0", newest, "2.0.0-rc1"),
		"/acme/terraform-provider-fixture/v" + newest + "/go.mod": goModRequiring(fixtureModule, crypto),
	}
}

// runException answers the registry and go.mod reads from url, and govulncheck's from a database holding GO-2026-5932 alone.
func runException(t *testing.T, report, root, url string, env ...string) (string, int) {
	t.Helper()
	db, err := filepath.Abs(filepath.Join("testdata", "vulndb"))
	if err != nil {
		t.Fatal(err)
	}
	env = append([]string{"PROVIDER_REGISTRY_URL=" + url, "PROVIDER_SOURCE_URL=" + url, "GOVULNCHECK_DB_URL=file://" + db}, env...)
	return runBash(t, env, "go-2026-5932-exception.sh", report, root)
}

func wantOutput(t *testing.T, out string, exit, wantExit int, want []string, notWant string) {
	t.Helper()
	if exit != wantExit {
		t.Errorf("exit = %d; want %d\n%s", exit, wantExit, out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output does not name %q:\n%s", w, out)
		}
	}
	if notWant != "" && strings.Contains(out, notWant) {
		t.Errorf("output carries %q:\n%s", notWant, out)
	}
}

func TestException_AcceptsTheAdvisoryOnlyInAProviderBinaryWithNoOpenpgpCode(t *testing.T) {
	plain, openpgp, nocrypto := fixtureBinary(t, fixtureModule, ""), fixtureBinary(t, fixtureModule, "openpgp"), fixtureBinary(t, fixtureModule, "nocrypto")
	netFinding := finding{"vuln", fixtureTarget, "CVE-2026-78669", "golang.org/x/net", "v0.59.0", "0.60.0"}
	cases := []struct {
		name     string
		binaries map[string]string
		findings []finding
		exit     int
		want     []string
	}{
		{name: "a stripped provider binary at x/crypto v0.58.0 with no openpgp code is accepted", binaries: map[string]string{fixtureTarget: plain},
			findings: []finding{advisoryIn(fixtureTarget)}, want: []string{
				"ran at go.mod-level precision because the binary is stripped, so it cannot show which packages are present, and it does not decide",
				"accepted GO-2026-5932 in " + fixtureTarget + ": its Go function table names ", " golang.org/x/crypto functions and 0 in golang.org/x/crypto/openpgp",
				"newest release 1.0.0 still requires golang.org/x/crypto v0.58.0"}},
		{name: "the same binary built to import openpgp is refused, naming its openpgp functions", binaries: map[string]string{fixtureTarget: openpgp},
			findings: []finding{advisoryIn(fixtureTarget)}, exit: 1,
			want: []string{"refused GO-2026-5932 in " + fixtureTarget + ": its Go function table names ", " functions in golang.org/x/crypto/openpgp:\n",
				"\n    golang.org/x/crypto/openpgp.ReadArmoredKeyRing\n"}},
		{name: "a binary whose table names no x/crypto function is refused", binaries: map[string]string{fixtureTarget: nocrypto},
			findings: []finding{advisoryIn(fixtureTarget)}, exit: 1,
			want: []string{"refused GO-2026-5932 in " + fixtureTarget + ": its Go function table names no golang.org/x/crypto function, so it proves nothing"}},
		{name: "the same finding in a binary outside the provider directory is refused", binaries: map[string]string{operatorTarget: plain},
			findings: []finding{advisoryIn(operatorTarget)}, exit: 1, want: []string{"refused GO-2026-5932 in " + operatorTarget}},
		{name: "a second finding with a fixed version in the provider binary is refused, naming it", binaries: map[string]string{fixtureTarget: plain},
			findings: []finding{advisoryIn(fixtureTarget), netFinding}, exit: 1,
			want: []string{"refused CVE-2026-78669 in " + fixtureTarget + ": golang.org/x/net v0.59.0 has a fix in 0.60.0"}},
		{name: "a secret Trivy found in the provider binary is refused", binaries: map[string]string{fixtureTarget: plain},
			findings: []finding{advisoryIn(fixtureTarget), {kind: "secret", target: fixtureTarget, id: "aws-access-key-id"}}, exit: 1,
			want: []string{"refused secret aws-access-key-id in " + fixtureTarget}},
		{name: "a provider binary missing from the image root is refused", binaries: map[string]string{},
			findings: []finding{advisoryIn(fixtureTarget)}, exit: 1, want: []string{"refused GO-2026-5932 in " + fixtureTarget + ": the image holds no such file"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, exit := runException(t, trivyReport(t, c.findings...), imageRoot(t, c.binaries), serve(t, fixtureNewest("1.0.0", "v0.58.0")))

			notWant := ""
			if c.exit != 0 && len(c.findings) == 1 {
				notWant = "accepted GO-2026-5932"
			}
			wantOutput(t, out, exit, c.exit, c.want, notWant)
		})
	}
}

func TestException_RefusesTheAWSProviderOnceItsNewestReleaseIsFixedAndStillAcceptsScaleway(t *testing.T) {
	aws := fixtureBinary(t, "github.com/hashicorp/terraform-provider-aws", "")
	scaleway := fixtureBinary(t, "github.com/scaleway/terraform-provider-scaleway/v2", "")
	url := serve(t, map[string]string{
		"/v1/providers/hashicorp/aws/versions":                 registryVersions("6.70.0", "6.71.0", "6.72.0"),
		"/hashicorp/terraform-provider-aws/v6.72.0/go.mod":     goModRequiring("github.com/hashicorp/terraform-provider-aws", "v0.59.0"),
		"/v1/providers/scaleway/scaleway/versions":             registryVersions("2.88.0", "2.89.0"),
		"/scaleway/terraform-provider-scaleway/v2.89.0/go.mod": goModRequiring("github.com/scaleway/terraform-provider-scaleway/v2", "v0.57.0"),
	})

	out, exit := runException(t, trivyReport(t, advisoryIn(awsTarget), advisoryIn(scalewayTarget)),
		imageRoot(t, map[string]string{awsTarget: aws, scalewayTarget: scaleway}), url)

	wantOutput(t, out, exit, 1, []string{
		"refused GO-2026-5932 in " + awsTarget + ": registry.opentofu.org/hashicorp/aws 6.72.0 requires golang.org/x/crypto v0.59.0, at or above the fix " + fixedCrypto +
			", so the exception no longer covers 6.71.0; move the block to registry.opentofu.org/hashicorp/aws 6.72.0",
		"accepted GO-2026-5932 in " + scalewayTarget,
		"registry.opentofu.org/scaleway/scaleway's newest release 2.89.0 still requires golang.org/x/crypto v0.57.0",
	}, "accepted GO-2026-5932 in "+awsTarget)
}

func TestException_OrdersTheNewestReleasesCryptoAgainstTheFix(t *testing.T) {
	plain := fixtureBinary(t, fixtureModule, "")
	cases := []struct {
		name, crypto string
		exit         int
		want         string
	}{
		{"a newer release still on v0.58.0 keeps the exception", "v0.58.0", 0, "accepted GO-2026-5932"},
		{"a newer release on an older pseudo-version keeps the exception", "v0.58.1-0.20261001000000-0123456789ab", 0, "accepted GO-2026-5932"},
		{"a newer release on the fixed pseudo-version ends it", fixedCrypto, 1, moveFixture},
		{"a newer release on the tag v0.58.1 ends it", "v0.58.1", 1, moveFixture},
		{"a newer release on v0.59.0 ends it", "v0.59.0", 1, moveFixture},
		{"a newer release requiring no x/crypto ends it", "", 1, moveFixture},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, exit := runException(t, trivyReport(t, advisoryIn(fixtureTarget)), imageRoot(t, map[string]string{fixtureTarget: plain}), serve(t, fixtureNewest("1.1.0", c.crypto)))

			wantOutput(t, out, exit, c.exit, []string{c.want}, "")
		})
	}
}

func TestException_FailsNamingAReadThatGetsNoAnswer(t *testing.T) {
	plain := fixtureBinary(t, fixtureModule, "")
	cases := []struct {
		name, read string
		routes     map[string]string
	}{
		{"the registry does not answer", "could not read %s/v1/providers/acme/fixture/versions", nil},
		{"the newest release's go.mod does not answer", "could not read %s/acme/terraform-provider-fixture/v1.1.0/go.mod",
			map[string]string{"/v1/providers/acme/fixture/versions": registryVersions("1.0.0", "1.1.0")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url := serve(t, c.routes)

			out, exit := runException(t, trivyReport(t, advisoryIn(fixtureTarget)), imageRoot(t, map[string]string{fixtureTarget: plain}), url)

			wantOutput(t, out, exit, 1, []string{fmt.Sprintf(c.read, url)}, "accepted GO-2026-5932")
		})
	}
}

func TestException_PrintsGovulncheckFailingAndLetsTheFunctionTableDecide(t *testing.T) {
	plain := fixtureBinary(t, fixtureModule, "")
	url := serve(t, fixtureNewest("1.0.0", "v0.58.0"))

	out, exit := runException(t, trivyReport(t, advisoryIn(fixtureTarget)), imageRoot(t, map[string]string{fixtureTarget: plain}), url, "GOVULNCHECK_DB_URL="+url)

	wantOutput(t, out, exit, 0, []string{"govulncheck could not scan it against " + url + ", so the function table alone decides", "accepted GO-2026-5932 in " + fixtureTarget}, "")
}

func TestException_RefusesAReportItCannotReadOrThatHoldsNoFinding(t *testing.T) {
	cases := []struct{ name, report, want string }{
		{"a report cut short", `{"Results": [`, "could not read the Trivy report %s, so no finding is accepted"},
		{"a report holding no finding", `{"SchemaVersion": 2, "Results": [{"Target": "` + fixtureTarget + `", "Type": "gobinary"}]}`,
			"the Trivy report %s holds no finding, so nothing in it explains why the scan failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := filepath.Join(t.TempDir(), "report.json")
			writeFile(t, report, c.report)

			out, exit := runException(t, report, t.TempDir(), serve(t, nil))

			wantOutput(t, out, exit, 1, []string{fmt.Sprintf(c.want, report)}, "accepted all")
		})
	}
}
