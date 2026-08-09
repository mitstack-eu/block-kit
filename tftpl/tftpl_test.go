package tftpl_test

// tftpl_test.go — ADR 0052 / MIT-1637.
//
// The escape test is the one that matters. `edge_config.sh.tftpl` writes a
// Caddyfile containing `default_sni $${PUBLIC_IP}` — that must reach the box as
// a SHELL expansion, not be interpolated away. Getting it wrong produces a
// Caddyfile bound to the wrong host and a box answering nothing on :443, which
// is the #526 symptom the script exists to fix.
//
// The rest are about refusing to guess: a blank substitution yields a
// valid-looking file that points nowhere, which is worse than an error.

import (
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/tftpl"
)

func TestRender_SubstitutesPlaceholders(t *testing.T) {
	got, err := tftpl.Render("reverse_proxy ${app_name}-app:8080", map[string]string{"app_name": "checkout"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "reverse_proxy checkout-app:8080" {
		t.Fatalf("got %q", got)
	}
}

// THE ESCAPE. `$${X}` is a literal `${X}` that the BOX resolves at run time.
func TestRender_EscapedPlaceholderSurvivesAsShellSyntax(t *testing.T) {
	got, err := tftpl.Render("default_sni $${PUBLIC_IP}", map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "default_sni ${PUBLIC_IP}" {
		t.Fatalf("got %q, want the shell expansion preserved verbatim", got)
	}
}

// Both in one line, which is how the real Caddyfile block reads.
func TestRender_EscapedAndInterpolatedTogether(t *testing.T) {
	got, err := tftpl.Render(
		"https://$${PUBLIC_IP} {\n  reverse_proxy ${app_name}-app:8080\n}",
		map[string]string{"app_name": "checkout"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://${PUBLIC_IP} {\n  reverse_proxy checkout-app:8080\n}"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A substituted VALUE must never be rescanned. A value that happened to contain
// `${...}` would otherwise be interpolated a second time — the classic template
// injection, here reachable from an app name.
func TestRender_SubstitutedValueIsNotRescanned(t *testing.T) {
	got, err := tftpl.Render("name=${app_name}", map[string]string{"app_name": "${domain}"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "name=${domain}" {
		t.Fatalf("got %q — a substituted value must be inserted verbatim, never re-interpolated", got)
	}
}

// A blank substitution produces `reverse_proxy -app:8080`: a valid-looking
// Caddyfile pointing at nothing. Refusing is the only safe answer.
func TestRender_MissingValueIsAnErrorNotAnEmptyString(t *testing.T) {
	_, err := tftpl.Render("reverse_proxy ${app_name}-app:8080", map[string]string{})
	if err == nil {
		t.Fatal("a missing value must be an error")
	}
	if !strings.Contains(err.Error(), "app_name") {
		t.Fatalf("the error must name the placeholder, got %v", err)
	}
}

// The caller and the script disagreeing about the contract is the drift the
// single-source rule exists to prevent, so it fails rather than passes quietly.
func TestRender_UnusedVariableIsAnError(t *testing.T) {
	_, err := tftpl.Render("hello", map[string]string{"region": "eu-central-1"})
	if err == nil {
		t.Fatal("a supplied-but-unused variable must be an error")
	}
	if !strings.Contains(err.Error(), "region") {
		t.Fatalf("the error must name the variable, got %v", err)
	}
}

// Silently ignoring a directive would emit a script with its conditional body
// unconditionally present — subtly wrong rather than obviously broken.
func TestRender_UnsupportedDirectiveIsRefused(t *testing.T) {
	_, err := tftpl.Render("%{ if x }a%{ endif }", map[string]string{})
	if err == nil {
		t.Fatal("an unimplemented directive must be refused, not passed through")
	}
}

// Lines matches the OpenTofu module's split("\n", templatefile(...)), which is
// the shape an SSM document's runCommand takes.
func TestLines_MatchesTheOpenTofuSplit(t *testing.T) {
	got, err := tftpl.Lines("a\nb ${x}\nc", map[string]string{"x": "1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b 1", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A script with no placeholders at all must render unchanged — the renderer is
// not allowed to normalise, reindent or trim anything it does not substitute.
func TestRender_LeavesEverythingElseByteIdentical(t *testing.T) {
	src := "#!/bin/bash\nset -euxo pipefail\n\n# a comment with $ and % and {braces}\nexit 0\n"
	got, err := tftpl.Render(src, map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != src {
		t.Fatalf("renderer altered non-placeholder text:\n got %q\nwant %q", got, src)
	}
}

// --- the one directive the real script uses ---------------------------------
//
// `edge_config.sh.tftpl` guards a Caddy site block with
// `%{ if domain != "" }`. Both branches are asserted because both ship: the
// default path is a bare-IP box with no domain, and getting THAT branch wrong
// would break every app that never set one.

func TestRender_ConditionalIncludesTheBodyWhenSet(t *testing.T) {
	got, err := tftpl.Render(
		"a\n%{ if domain != \"\" }\n${domain} {\n}\n%{ endif }\nb",
		map[string]string{"domain": "app.example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "a\n\napp.example.com {\n}\n\nb"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRender_ConditionalDropsTheBodyWhenEmpty(t *testing.T) {
	got, err := tftpl.Render(
		"a\n%{ if domain != \"\" }\n${domain} {\n}\n%{ endif }\nb",
		map[string]string{"domain": ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "a\n\nb"
	if got != want {
		t.Fatalf("got %q, want %q — the markers and the guarded body go, the surrounding newlines stay", got, want)
	}
}

// The no-domain path is the DEFAULT, so a variable consumed only by a condition
// must not read as unused. Getting this wrong would fail every app that never
// set a domain — the common case, failing while the rare one worked.
func TestRender_ConditionOnlyVariableIsNotReportedUnused(t *testing.T) {
	_, err := tftpl.Render(
		"a\n%{ if domain != \"\" }\n${domain}\n%{ endif }\nb",
		map[string]string{"domain": ""})
	if err != nil {
		t.Fatalf("a variable consumed only by a condition must count as used: %v", err)
	}
}

// Treating an unknown condition variable as false would silently omit the
// consumer's domain block and serve on a bare IP with nothing reported.
func TestRender_UnknownConditionVariableIsAnError(t *testing.T) {
	_, err := tftpl.Render("%{ if nope != \"\" }x%{ endif }", map[string]string{})
	if err == nil {
		t.Fatal("an unknown condition variable must be an error, not false")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("the error must name it, got %v", err)
	}
}

// Two blocks must resolve independently. A greedy match would swallow the text
// between them and drop it whenever the FIRST condition was false.
func TestRender_TwoConditionalsDoNotMerge(t *testing.T) {
	got, err := tftpl.Render(
		"%{ if a != \"\" }A%{ endif }MIDDLE%{ if b != \"\" }B%{ endif }",
		map[string]string{"a": "", "b": "yes"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "MIDDLEB" {
		t.Fatalf("got %q, want %q — a greedy match would have eaten MIDDLE", got, "MIDDLEB")
	}
}

// A form this package does not implement must be refused rather than emitted as
// literal text, which would put `%{ for ... }` into a shell script.
func TestRender_UnimplementedDirectiveFormIsRefused(t *testing.T) {
	for _, src := range []string{
		"%{ for x in y }a%{ endfor }",
		"%{ if domain == \"prod\" }a%{ endif }",
		"%{~ if domain != \"\" ~}a%{ endif }",
	} {
		if _, err := tftpl.Render(src, map[string]string{"domain": "x"}); err == nil {
			t.Errorf("must refuse unimplemented form: %q", src)
		}
	}
}
