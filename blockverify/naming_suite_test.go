package main

// naming_suite_test.go pins the naming suite: a manifest may declare
// spec.naming.appName, listing each name the block derives from the app name
// as a template carrying {appName} with the provider's limit for that
// resource, plus the maxLength it claims for the app name.
//
//   - the limit is the smallest, over the templates, of the template's limit
//     minus the length of its fixed text (the template with {appName}
//     removed), and the suite fails when maxLength differs, naming the
//     template that sets the limit and the computed value;
//   - a template with no {appName}, a maxLength with no derivedNames behind
//     it, and a template whose fixed text alone reaches its limit all fail;
//   - a manifest with no spec.naming is skipped, since it declares no limit,
//     and a manifest whose declaration cannot be read fails.
//
// The suite is called directly as namingSuite(target). The Run-level test at
// the end pins its place in the report and its effect on the verdict.

import (
	"strings"
	"testing"
)

// ecrPushTemplate is aws-app-infra's GitHub Actions ECR push role name: 37
// fixed characters around the app name, inside IAM's 64-character limit, so
// the app name may run to 27.
const ecrPushTemplate = "mitstack-app-{appName}-github-actions-ecr-push"

// namingSpec returns spec.naming lines declaring maxLength and the given
// derivedNames items, each already written as YAML lines indented to sit
// under derivedNames:.
func namingSpec(maxLength string, items ...string) string {
	s := "  naming:\n    appName:\n      maxLength: " + maxLength + "\n"
	if len(items) > 0 {
		s += "      derivedNames:\n" + strings.Join(items, "")
	}
	return s
}

// derivedName returns one derivedNames item for template and limit.
func derivedName(template, limit string) string {
	return "        - template: \"" + template + "\"\n          limit: " + limit + "\n"
}

// awsAppInfraNamingSpec is aws-app-infra's declaration, maxLength 27.
var awsAppInfraNamingSpec = namingSpec("27", derivedName(ecrPushTemplate, "64"))

func requireNamingStatus(t *testing.T, got SuiteResult, status string) {
	t.Helper()
	if got.Name != "naming" {
		t.Errorf("suite name = %q, want %q", got.Name, "naming")
	}
	if got.Status != status {
		t.Errorf("naming status = %q, want %q; detail: %s", got.Status, status, got.Detail)
	}
	if got.Detail == "" {
		t.Error("the naming suite must say what it found in detail")
	}
}

func requireNamingFail(t *testing.T, got SuiteResult, wants ...string) {
	t.Helper()
	requireNamingStatus(t, got, "fail")
	for _, want := range wants {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("naming detail = %q, want it to contain %q", got.Detail, want)
		}
	}
}

// --- the computed limit ---------------------------------------------------

func TestNamingSuite_AWSAppInfraDeclarationPasses(t *testing.T) {
	requireNamingStatus(t, namingSuite(manifestDir(t, manifestHead+awsAppInfraNamingSpec)), "pass")
}

func TestNamingSuite_AMaxLengthThatDiffersFromTheComputedLimitFails(t *testing.T) {
	spec := namingSpec("40", derivedName(ecrPushTemplate, "64"))
	requireNamingFail(t, namingSuite(manifestDir(t, manifestHead+spec)), ecrPushTemplate, "27")
}

// TestNamingSuite_TheLimitIsTheSmallestOverTemplates lists the template that
// sets the limit after one that allows a longer app name, so neither the
// first template nor the largest limit can pass for the smallest.
func TestNamingSuite_TheLimitIsTheSmallestOverTemplates(t *testing.T) {
	const roomyTemplate = "{appName}-db"
	items := []string{derivedName(roomyTemplate, "63"), derivedName(ecrPushTemplate, "64")}

	t.Run("maxLength is the smallest", func(t *testing.T) {
		requireNamingStatus(t, namingSuite(manifestDir(t, manifestHead+namingSpec("27", items...))), "pass")
	})
	t.Run("maxLength is a larger template's limit", func(t *testing.T) {
		requireNamingFail(t, namingSuite(manifestDir(t, manifestHead+namingSpec("60", items...))), ecrPushTemplate, "27")
	})
}

// --- refusals -------------------------------------------------------------

func TestNamingSuite_AMaxLengthWithNoDerivedNamesFails(t *testing.T) {
	requireNamingFail(t, namingSuite(manifestDir(t, manifestHead+namingSpec("27"))), "derivedNames")
}

func TestNamingSuite_ATemplateWithoutThePlaceholderFails(t *testing.T) {
	const template = "mitstack-app-github-actions-ecr-push"
	spec := namingSpec("27", derivedName(template, "64"))
	requireNamingFail(t, namingSuite(manifestDir(t, manifestHead+spec)), template, "{appName}")
}

// TestNamingSuite_ATemplateWhoseFixedTextReachesItsLimitFails covers fixed
// text equal to the limit, which leaves no character for the app name, as
// well as fixed text over it.
func TestNamingSuite_ATemplateWhoseFixedTextReachesItsLimitFails(t *testing.T) {
	for _, limit := range []string{"37", "30"} {
		t.Run("limit "+limit, func(t *testing.T) {
			spec := namingSpec("0", derivedName(ecrPushTemplate, limit))
			requireNamingFail(t, namingSuite(manifestDir(t, manifestHead+spec)), ecrPushTemplate, limit)
		})
	}
}

// --- absence and unreadable declarations ----------------------------------

func TestNamingSuite_NoSpecNamingIsSkipped(t *testing.T) {
	requireNamingStatus(t, namingSuite(manifestDir(t, manifestHead)), "skipped")
}

func TestNamingSuite_NoManifestIsSkipped(t *testing.T) {
	requireNamingStatus(t, namingSuite(t.TempDir()), "skipped")
}

func TestNamingSuite_AnUnreadableDeclarationFails(t *testing.T) {
	spec := namingSpec("twenty-seven", derivedName(ecrPushTemplate, "64"))
	requireNamingFail(t, namingSuite(manifestDir(t, manifestHead+spec)), "spec.naming.appName")
}

// --- the whole command ----------------------------------------------------

func TestRun_NamingSuiteRunsAfterManifestAndDecidesTheVerdict(t *testing.T) {
	cases := []struct {
		label    string
		spec     string
		status   string
		verdict  string
		exitCode int
	}{
		{"no spec.naming", "", "skipped", "pass", 0},
		{"aws-app-infra declaration", awsAppInfraNamingSpec, "pass", "pass", 0},
		{"maxLength 40", namingSpec("40", derivedName(ecrPushTemplate, "64")), "fail", "fail", 1},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			files := goldenFixtureFiles(t)
			files["manifest.yaml"] = manifestYAML + c.spec
			dir := newFixtureFrom(t, files)

			stdout, exit := runBlockverify(t, "--json", "--standalone", dir)
			if exit != c.exitCode {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s", exit, c.exitCode, stdout)
			}
			r := decodeReport(t, stdout)
			if r.Verdict != c.verdict {
				t.Errorf("verdict = %q, want %q", r.Verdict, c.verdict)
			}
			if got := suiteByName(t, r, "naming"); got.Status != c.status {
				t.Errorf("naming status = %q, want %q; detail: %s", got.Status, c.status, got.Detail)
			}
		})
	}
}
