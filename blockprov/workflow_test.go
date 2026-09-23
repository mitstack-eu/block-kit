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
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// --- the tag-exists guard step (publish job, id: existing) ---
//
// A republish of an already-tagged version must push nothing and write no
// provenance record. The tests below pin two things: the guard step's own
// run: script (executed for real, against a stub `aws`, since its exit
// code and $GITHUB_OUTPUT are the whole contract), and the `if:` on every
// step downstream of it that must skip once the tag is already published.

// stepBlocks splits a jobs-level YAML block into one text chunk per "- "
// list item (each step), by indentation: only lines whose "- " marker sits
// at the minimum indent used by any item start a new chunk, so a step's own
// nested lines (its "with:", "run: |" body, and so on) stay inside the
// chunk that contains them.
func stepBlocks(jobBlock string) []string {
	lines := strings.Split(jobBlock, "\n")
	itemPattern := regexp.MustCompile(`^(\s*)-\s`)

	minIndent := -1
	for _, line := range lines {
		if m := itemPattern.FindStringSubmatch(line); m != nil {
			if ind := len(m[1]); minIndent == -1 || ind < minIndent {
				minIndent = ind
			}
		}
	}

	var starts []int
	for i, line := range lines {
		if m := itemPattern.FindStringSubmatch(line); m != nil && len(m[1]) == minIndent {
			starts = append(starts, i)
		}
	}

	var blocks []string
	for idx, start := range starts {
		end := len(lines)
		if idx+1 < len(starts) {
			end = starts[idx+1]
		}
		blocks = append(blocks, strings.Join(lines[start:end], "\n"))
	}
	return blocks
}

// indexOfStepByID returns the index into blocks of the step carrying
// "id: <id>", or -1.
func indexOfStepByID(blocks []string, id string) int {
	idPattern := regexp.MustCompile(`(?m)^\s*id:\s*` + regexp.QuoteMeta(id) + `\s*$`)
	for i, b := range blocks {
		if idPattern.MatchString(b) {
			return i
		}
	}
	return -1
}

// indexOfStepByName returns the index into blocks of the step whose
// "- name:" line matches name exactly, or -1.
func indexOfStepByName(blocks []string, name string) int {
	namePattern := regexp.MustCompile(`(?m)^\s*-?\s*name:\s*"?` + regexp.QuoteMeta(name) + `"?\s*$`)
	for i, b := range blocks {
		if namePattern.MatchString(b) {
			return i
		}
	}
	return -1
}

// ifOf returns the trimmed expression after a step's `if:` key, with an
// optional `${{ ... }}` wrapper stripped, or "" if the step has none.
func ifOf(stepBlock string) string {
	m := regexp.MustCompile(`(?m)^\s*if:\s*(.+)\s*$`).FindStringSubmatch(stepBlock)
	if m == nil {
		return ""
	}
	expr := strings.TrimSpace(m[1])
	expr = strings.TrimPrefix(expr, "${{")
	expr = strings.TrimSuffix(expr, "}}")
	return strings.TrimSpace(expr)
}

// runScript returns the dedented body of a step's `run: |` literal block,
// reusing block() to pull out everything nested under the step's own
// `run:` key and then stripping the common leading indent so the result is
// a plain, directly executable shell script.
func runScript(stepBlock string) string {
	body := block(stepBlock, "run")
	lines := strings.Split(body, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	minIndent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if ind := indentOf(l); minIndent == -1 || ind < minIndent {
			minIndent = ind
		}
	}
	if minIndent < 0 {
		minIndent = 0
	}
	for i, l := range lines {
		if len(l) >= minIndent {
			lines[i] = l[minIndent:]
		} else {
			lines[i] = strings.TrimLeft(l, " ")
		}
	}
	return strings.Join(lines, "\n")
}

// publishStepBlocks reads the workflow and returns the publish job's steps,
// split one chunk per step.
func publishStepBlocks(t *testing.T) []string {
	t.Helper()
	text := readWorkflow(t)
	publish := block(block(text, "jobs"), "publish")
	if publish == "" {
		t.Fatal("no publish job block found")
	}
	return stepBlocks(publish)
}

// existingStepRunScript locates the publish job's `id: existing` step and
// returns its dedented run: script. It fails the test (not the helper's
// caller) when the step or its script is missing, which is the RED signal
// before code-author adds the step.
func existingStepRunScript(t *testing.T) string {
	t.Helper()
	blocks := publishStepBlocks(t)
	idx := indexOfStepByID(blocks, "existing")
	if idx < 0 {
		t.Fatal("publish job has no step with `id: existing` — the tag-exists guard step is missing")
	}
	script := runScript(blocks[idx])
	if strings.TrimSpace(script) == "" {
		t.Fatal("publish job's `id: existing` step has no run: script")
	}
	return script
}

// testDigest is a syntactically valid 64-hex-character sha256 digest, used
// as the stub registry's canned "tag exists" answer.
var testDigest = "sha256:" + strings.Repeat("ab", 32)

// writeExecutable writes an executable file at path.
func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// guardResult carries everything a test needs to assert on a run of the
// guard step's script.
type guardResult struct {
	stdout, stderr, combined string
	exitCode                 int
	githubOutput             string
	awsArgv                  string
}

// runGuardScript extracts the publish job's `id: existing` run: script,
// replaces the `${{ inputs.ecr-repository }}` expression with a literal
// repository name (and exports ECR_REPOSITORY too, so the test accepts
// either an inline expression or an env-var-based implementation), and
// executes it with bash against a stub `aws` that argv-records itself and
// answers per awsMode ("exists", "absent", or "fail").
func runGuardScript(t *testing.T, version, awsMode string) guardResult {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}

	script := existingStepRunScript(t)
	script = strings.ReplaceAll(script, "${{ inputs.ecr-repository }}", "mitstack-postgres-db")

	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "guard.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatalf("writing guard script: %v", err)
	}

	workDir := filepath.Join(tmp, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "spec:\n  version: " + version + "\n"
	if err := os.WriteFile(filepath.Join(workDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	stubDir := filepath.Join(tmp, "stub")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(tmp, "aws-argv")
	stub := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$AWS_ARGV_FILE\"\n" +
		"case \"$AWS_MODE\" in\n" +
		"  exists) echo \"$AWS_DIGEST\"; exit 0 ;;\n" +
		"  absent) echo \"None\"; exit 0 ;;\n" +
		"  fail) echo \"boom: access denied\" >&2; exit 254 ;;\n" +
		"esac\n"
	writeExecutable(t, filepath.Join(stubDir, "aws"), stub)

	outputFile := filepath.Join(tmp, "github-output")
	if err := os.WriteFile(outputFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+":"+os.Getenv("PATH"),
		"GITHUB_OUTPUT="+outputFile,
		"ECR_REPOSITORY=mitstack-postgres-db",
		"AWS_MODE="+awsMode,
		"AWS_DIGEST="+testDigest,
		"AWS_ARGV_FILE="+argvFile,
	)

	var stdout, stderr, combined strings.Builder
	cmd.Stdout = io.MultiWriter(&stdout, &combined)
	cmd.Stderr = io.MultiWriter(&stderr, &combined)

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("running guard script: %v", err)
		}
	}

	outputBytes, _ := os.ReadFile(outputFile)
	argvBytes, _ := os.ReadFile(argvFile)

	return guardResult{
		stdout:       stdout.String(),
		stderr:       stderr.String(),
		combined:     combined.String(),
		exitCode:     exitCode,
		githubOutput: string(outputBytes),
		awsArgv:      string(argvBytes),
	}
}

func TestWorkflow_ExistingStepPrintsAndRecordsWhenTagExists(t *testing.T) {
	res := runGuardScript(t, "1.2.0", "exists")

	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; combined output:\n%s", res.exitCode, res.combined)
	}
	want := "already published: v1.2.0 " + testDigest
	if !strings.Contains(res.stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", res.stdout, want)
	}
	if !strings.Contains(res.githubOutput, "published=true") {
		t.Errorf("$GITHUB_OUTPUT = %q, want it to contain %q", res.githubOutput, "published=true")
	}
	if !strings.Contains(res.githubOutput, "digest="+testDigest) {
		t.Errorf("$GITHUB_OUTPUT = %q, want it to contain %q", res.githubOutput, "digest="+testDigest)
	}
	for _, want := range []string{"ecr", "batch-get-image", "--repository-name", "mitstack-postgres-db", "imageTag=v1.2.0"} {
		if !strings.Contains(res.awsArgv, want) {
			t.Errorf("aws argv = %q, want it to contain %q", res.awsArgv, want)
		}
	}
}

func TestWorkflow_ExistingStepPublishesFalseWhenTagAbsent(t *testing.T) {
	res := runGuardScript(t, "1.2.0", "absent")

	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; combined output:\n%s", res.exitCode, res.combined)
	}
	if strings.Contains(res.stdout, "already published") {
		t.Errorf("stdout = %q, must not claim a tag as already published when the registry said None", res.stdout)
	}
	if !strings.Contains(res.githubOutput, "published=false") {
		t.Errorf("$GITHUB_OUTPUT = %q, want it to contain %q", res.githubOutput, "published=false")
	}
}

func TestWorkflow_ExistingStepFailsClosedWhenTheProbeCannotAnswer(t *testing.T) {
	res := runGuardScript(t, "1.2.0", "fail")

	if res.exitCode == 0 {
		t.Fatalf("exit code = 0, want non-zero — a probe that cannot answer must never be read as \"not published\"; combined output:\n%s", res.combined)
	}
	if !strings.Contains(res.combined, "v1.2.0") {
		t.Errorf("combined output = %q, want it to name the tag v1.2.0 it could not confirm", res.combined)
	}
}

func TestWorkflow_ExistingStepAVersionBumpStillPublishes(t *testing.T) {
	res := runGuardScript(t, "1.3.0", "absent")

	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; combined output:\n%s", res.exitCode, res.combined)
	}
	if strings.Contains(res.stdout, "already published") {
		t.Errorf("stdout = %q, must not claim a tag as already published when the registry said None", res.stdout)
	}
	if !strings.Contains(res.githubOutput, "published=false") {
		t.Errorf("$GITHUB_OUTPUT = %q, want it to contain %q", res.githubOutput, "published=false")
	}
	if !strings.Contains(res.awsArgv, "imageTag=v1.3.0") {
		t.Errorf("aws argv = %q, want the bumped version's tag imageTag=v1.3.0 — a version bump must still probe and publish through the same workflow", res.awsArgv)
	}
}

func TestWorkflow_ExistingStepPrecedesBuildAndPushImage(t *testing.T) {
	blocks := publishStepBlocks(t)
	existingIdx := indexOfStepByID(blocks, "existing")
	if existingIdx < 0 {
		t.Fatal("publish job has no step with `id: existing` — the tag-exists guard step is missing")
	}
	buildIdx := indexOfStepByName(blocks, "Build and push image")
	if buildIdx < 0 {
		t.Fatal(`publish job has no "Build and push image" step`)
	}
	if existingIdx >= buildIdx {
		t.Errorf("the `id: existing` guard step is at position %d, the \"Build and push image\" step is at position %d — the guard must run first", existingIdx, buildIdx)
	}
}

func TestWorkflow_StepsAfterTheGuardSkipWhenTheTagIsAlreadyPublished(t *testing.T) {
	blocks := publishStepBlocks(t)
	const wantIf = "steps.existing.outputs.published != 'true'"
	guardedNames := []string{
		"Build and push image",
		"Determine the kit version pinned in go.mod",
		"blockverify (standalone, for blockprov build's report)",
		"blockprov build",
		"Fetch ECR password",
		"blockprov push",
	}
	for _, name := range guardedNames {
		idx := indexOfStepByName(blocks, name)
		if idx < 0 {
			t.Errorf("publish job has no step named %q", name)
			continue
		}
		if got := ifOf(blocks[idx]); got != wantIf {
			t.Errorf("step %q has if: %q, want %q — it must skip once the tag is already published", name, got, wantIf)
		}
	}
}

func TestWorkflow_CleanUpKitTokenStepStillRunsAlways(t *testing.T) {
	blocks := publishStepBlocks(t)
	idx := indexOfStepByName(blocks, "Clean up the kit token file")
	if idx < 0 {
		t.Fatal(`publish job has no "Clean up the kit token file" step`)
	}
	if got := ifOf(blocks[idx]); got != "always()" {
		t.Errorf(`step "Clean up the kit token file" has if: %q, want "always()" — it must clean up the secret file whether or not the tag was already published`, got)
	}
}
