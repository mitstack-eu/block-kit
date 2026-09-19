package main

// workflow_test.go is a structural guard over
// ../.github/workflows/publish-block.yml (MIT-3097): a Go test, not a shell
// script, because the kit's shell guards under scripts/ are run by nothing
// (not go test ./..., not the kit's own ci.yml). It parses by indentation
// with plain string handling, the way scripts/check-release-workflow_test.sh
// extracts blocks from ci.yml — no YAML library, since the kit is
// stdlib-only.
//
// The workflow file itself does not exist yet, so every test below fails on
// "reading ../.github/workflows/publish-block.yml" until code-author adds
// it. That is the intended RED signal.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const workflowPath = "../.github/workflows/publish-block.yml"

// indentOf returns the number of leading spaces on line.
func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// block returns the lines nested under "<key>:" inside text, regardless of
// text's own indentation: every subsequent line more indented than the key
// line, up to (but excluding) the first later non-blank line at or above
// the key's own indent. Composes: feeding one call's result into another
// reaches a nested key, the way block(block(text, "jobs"), "conformance")
// does below. Mirrors check-release-workflow_test.sh's awk block().
func block(text, key string) string {
	lines := strings.Split(text, "\n")
	var out []string
	found := false
	keyIndent := 0
	keyPattern := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `:`)
	for _, line := range lines {
		if !found {
			if keyPattern.MatchString(line) {
				keyIndent = indentOf(line)
				found = true
			}
			continue
		}
		if strings.TrimSpace(line) != "" && indentOf(line) <= keyIndent {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// jobNames returns the top-level job keys directly under a jobs: block
// (indent 2 inside it, i.e. "  <name>:" lines).
func jobNames(jobsBlock string) []string {
	var names []string
	namePattern := regexp.MustCompile(`^\s\s(\S[^:]*):`)
	for _, line := range strings.Split(jobsBlock, "\n") {
		if m := namePattern.FindStringSubmatch(line); m != nil {
			names = append(names, m[1])
		}
	}
	return names
}

func readWorkflow(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("reading %s: %v", workflowPath, err)
	}
	return string(raw)
}

func TestWorkflow_DeclaresWorkflowCall(t *testing.T) {
	text := readWorkflow(t)
	onBlock := block(text, "on")
	if !strings.Contains(onBlock, "workflow_call") && !strings.Contains(text, "on: workflow_call") {
		t.Errorf("%s must declare `on: workflow_call`, got on: block:\n%s", workflowPath, onBlock)
	}
}

func TestWorkflow_HasConformanceAndPublishJobs(t *testing.T) {
	text := readWorkflow(t)
	jobs := block(text, "jobs")
	names := jobNames(jobs)

	hasConformance, hasPublish := false, false
	for _, n := range names {
		if n == "conformance" {
			hasConformance = true
		}
		if n == "publish" {
			hasPublish = true
		}
	}
	if !hasConformance {
		t.Errorf("jobs: must include a %q job, found: %v", "conformance", names)
	}
	if !hasPublish {
		t.Errorf("jobs: must include a %q job, found: %v", "publish", names)
	}
}

func TestWorkflow_PublishNeedsConformance(t *testing.T) {
	text := readWorkflow(t)
	publish := block(block(text, "jobs"), "publish")
	if publish == "" {
		t.Fatal("no publish job block found")
	}
	needsPattern := regexp.MustCompile(`(?m)^\s*needs:\s*(.+)$`)
	m := needsPattern.FindStringSubmatch(publish)
	if m == nil {
		t.Fatal("publish job has no `needs:` key")
	}
	needs := strings.Trim(strings.TrimSpace(m[1]), "[]")
	if !strings.Contains(needs, "conformance") {
		t.Errorf("publish job's needs = %q, want it to name conformance — a red conformance run must never start publish", needs)
	}
}

func TestWorkflow_ConformanceJobRunsVetTestAndBlockverify(t *testing.T) {
	text := readWorkflow(t)
	conformance := block(block(text, "jobs"), "conformance")
	if conformance == "" {
		t.Fatal("no conformance job block found")
	}
	for _, want := range []string{"go vet ./...", "go test ./...", "blockverify"} {
		if !strings.Contains(conformance, want) {
			t.Errorf("conformance job must run %q, job block:\n%s", want, conformance)
		}
	}
	if !strings.Contains(conformance, "--standalone") || !strings.Contains(conformance, "--json") {
		t.Error("the blockverify invocation must run --standalone --json, matching the CLI's only supported mode")
	}
}

func TestWorkflow_ConformanceJobNeverPushesOrWritesARecord(t *testing.T) {
	text := readWorkflow(t)
	conformance := block(block(text, "jobs"), "conformance")
	if conformance == "" {
		t.Fatal("no conformance job block found")
	}
	for _, forbidden := range []string{"docker push", "blockprov push", "blockprov build"} {
		if strings.Contains(conformance, forbidden) {
			t.Errorf("conformance job must never run %q — a red run must produce no artefact beyond its report", forbidden)
		}
	}
}

func TestWorkflow_PublishJobPushesAndWritesTheRecord(t *testing.T) {
	text := readWorkflow(t)
	publish := block(block(text, "jobs"), "publish")
	if publish == "" {
		t.Fatal("no publish job block found")
	}
	for _, want := range []string{"docker push", "blockprov build", "blockprov push"} {
		if !strings.Contains(publish, want) {
			t.Errorf("publish job must run %q, job block:\n%s", want, publish)
		}
	}
}

func TestWorkflow_GrantsIDTokenWrite(t *testing.T) {
	text := readWorkflow(t)
	if !strings.Contains(text, "id-token: write") {
		t.Error("the workflow must grant id-token: write somewhere — publish mints the OIDC token this whole record depends on")
	}
}

func TestWorkflow_EveryUsesIsPinnedToACommitSha(t *testing.T) {
	text := readWorkflow(t)
	usesPattern := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*(\S+)`)
	shaPattern := regexp.MustCompile(`^[0-9a-f]{40}$`)

	matches := usesPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		t.Fatal("workflow has no `uses:` steps at all — nothing to pin")
	}
	for _, m := range matches {
		ref := m[1]
		at := strings.LastIndex(ref, "@")
		if at < 0 {
			t.Errorf("uses: %q has no @ref at all, want a commit sha", ref)
			continue
		}
		sha := ref[at+1:]
		if !shaPattern.MatchString(sha) {
			t.Errorf("uses: %q is pinned to %q, want a 40-character commit sha, not a tag", ref, sha)
		}
	}
}

func TestWorkflow_ReportIsUploadedAsAnArtifactRegardlessOfVerdict(t *testing.T) {
	text := readWorkflow(t)
	conformance := block(block(text, "jobs"), "conformance")
	if conformance == "" {
		t.Fatal("no conformance job block found")
	}
	if !strings.Contains(conformance, "upload-artifact") {
		t.Fatal("conformance job must upload the blockverify report as an artifact")
	}
	if !strings.Contains(conformance, "if: always()") {
		t.Error("the report upload must run with if: always() — a failing run's report is exactly the one that must not be lost")
	}
}

// sanity-check the test's own regexes against a known-shaped fixture, so a
// failure above is trusted to mean the workflow, not a bug in block().
func TestBlockHelper_ExtractsNestedKeysByIndentation(t *testing.T) {
	sample := strings.Join([]string{
		"jobs:",
		"  conformance:",
		"    needs: []",
		"    steps:",
		"      - run: go vet ./...",
		"  publish:",
		"    needs: conformance",
		"    steps:",
		"      - run: docker push",
	}, "\n")

	jobs := block(sample, "jobs")
	names := jobNames(jobs)
	if len(names) != 2 || names[0] != "conformance" || names[1] != "publish" {
		t.Fatalf("jobNames = %v, want [conformance publish]", names)
	}

	publish := block(jobs, "publish")
	if !strings.Contains(publish, "docker push") {
		t.Fatalf("block(jobs, publish) = %q, want it to contain the publish job's own steps", publish)
	}
	if strings.Contains(publish, "go vet") {
		t.Fatalf("block(jobs, publish) leaked the conformance job's content: %q", publish)
	}
}
