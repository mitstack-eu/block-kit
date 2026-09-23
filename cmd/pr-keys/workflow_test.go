package main

// workflow_test.go is a structural guard over ../../.github/workflows/ci.yml
// for the pr-key-gate job: a Go test, not a shell script, following
// cmd/board-delivery/workflow_test.go's idiom — indentation-based parsing
// with plain string handling, no YAML library, since the kit is
// stdlib-only. Duplicated here rather than shared because these helpers live
// inside this package's own package main and cannot be imported from
// board-delivery's.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const ciWorkflowPath = "../../.github/workflows/ci.yml"

// prKeyGateJob is the job key this guard requires. Chosen to name the
// outcome (gating on a key that reaches the board), not the binary that
// performs it.
const prKeyGateJob = "pr-key-gate"

func ciIndentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// ciBlock returns the lines nested under "<key>:" inside text, mirroring
// blockprov/workflow_test.go's block().
func ciBlock(text, key string) string {
	lines := strings.Split(text, "\n")
	var out []string
	found := false
	keyIndent := 0
	keyPattern := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `:`)
	for _, line := range lines {
		if !found {
			if keyPattern.MatchString(line) {
				keyIndent = ciIndentOf(line)
				found = true
			}
			continue
		}
		if strings.TrimSpace(line) != "" && ciIndentOf(line) <= keyIndent {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func readCIWorkflow(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(ciWorkflowPath)
	if err != nil {
		t.Fatalf("reading %s: %v", ciWorkflowPath, err)
	}
	return string(raw)
}

func prKeyGateBlock(t *testing.T, text string) string {
	t.Helper()
	job := ciBlock(ciBlock(text, "jobs"), prKeyGateJob)
	if job == "" {
		t.Fatalf("no %q job found under jobs: in %s", prKeyGateJob, ciWorkflowPath)
	}
	return job
}

func testJobBlock(t *testing.T, text string) string {
	t.Helper()
	job := ciBlock(ciBlock(text, "jobs"), "test")
	if job == "" {
		t.Fatalf("no %q job found under jobs: in %s", "test", ciWorkflowPath)
	}
	return job
}

func firstUsesSHA(text, action string) string {
	pattern := regexp.MustCompile(`uses:\s*` + regexp.QuoteMeta(action) + `@(\S+)`)
	m := pattern.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestWorkflow_PRKeyGateJobExists(t *testing.T) {
	text := readCIWorkflow(t)
	prKeyGateBlock(t, text)
}

func TestWorkflow_PRKeyGateRunsOnlyOnPullRequest(t *testing.T) {
	text := readCIWorkflow(t)
	job := prKeyGateBlock(t, text)
	ifPattern := regexp.MustCompile(`(?m)^\s*if:\s*(.+)$`)
	m := ifPattern.FindStringSubmatch(job)
	if m == nil {
		t.Fatalf("%s job has no if: gate — it must run only on a pull_request, never on a push", prKeyGateJob)
	}
	cond := m[1]
	if !strings.Contains(cond, "pull_request") {
		t.Errorf("%s job's if: = %q, want it to require the pull_request event", prKeyGateJob, cond)
	}
}

func TestWorkflow_PRKeyGateNeedsNoSecrets(t *testing.T) {
	text := readCIWorkflow(t)
	job := prKeyGateBlock(t, text)
	if strings.Contains(job, "secrets.") {
		t.Errorf("%s job references secrets. — it must run with no secrets at all, job block:\n%s", prKeyGateJob, job)
	}
}

func TestWorkflow_PRKeyGateChecksOutFullHistory(t *testing.T) {
	text := readCIWorkflow(t)
	job := prKeyGateBlock(t, text)
	if !strings.Contains(job, "fetch-depth: 0") {
		t.Errorf("%s job must check out with fetch-depth: 0 — the base..head range needs history a shallow checkout has not", prKeyGateJob)
	}
}

// TestWorkflow_PRKeyGateReadsTitleFromThePullRequestEvent pins that the
// title comes from the pull_request event, never from a commit — a branch
// name folded into a merge or squash commit is not delivery intent.
func TestWorkflow_PRKeyGateReadsTitleFromThePullRequestEvent(t *testing.T) {
	text := readCIWorkflow(t)
	job := prKeyGateBlock(t, text)
	if !strings.Contains(job, "github.event.pull_request.title") {
		t.Errorf("%s job must read the PR title from github.event.pull_request.title, job block:\n%s", prKeyGateJob, job)
	}
	if strings.Contains(job, "head_commit") {
		t.Errorf("%s job references head_commit — the title must come from the pull request event, never from a commit, job block:\n%s", prKeyGateJob, job)
	}
}

func TestWorkflow_PRKeyGateUsesBaseAndHeadFromThePullRequestEvent(t *testing.T) {
	text := readCIWorkflow(t)
	job := prKeyGateBlock(t, text)
	if !strings.Contains(job, "github.event.pull_request.base.sha") {
		t.Errorf("%s job must pass the pull request's base sha into the gate, job block:\n%s", prKeyGateJob, job)
	}
	if !strings.Contains(job, "github.event.pull_request.head.sha") {
		t.Errorf("%s job must pass the pull request's head sha into the gate, job block:\n%s", prKeyGateJob, job)
	}
}

func TestWorkflow_PRKeyGateInvokesTheCommand(t *testing.T) {
	text := readCIWorkflow(t)
	job := prKeyGateBlock(t, text)
	if !strings.Contains(job, "cmd/pr-keys") {
		t.Errorf("%s job must invoke the cmd/pr-keys command, job block:\n%s", prKeyGateJob, job)
	}
}

func TestWorkflow_PRKeyGateHasNoContinueOnError(t *testing.T) {
	text := readCIWorkflow(t)
	job := prKeyGateBlock(t, text)
	if strings.Contains(job, "continue-on-error") {
		t.Errorf("%s job must not carry continue-on-error — a missing key must be a red check, not a silent one", prKeyGateJob)
	}
}

func TestWorkflow_PRKeyGatePinsCheckoutToTheSameSHAAsTest(t *testing.T) {
	text := readCIWorkflow(t)
	testJob := testJobBlock(t, text)
	job := prKeyGateBlock(t, text)
	wantSHA := firstUsesSHA(testJob, "actions/checkout")
	if wantSHA == "" {
		t.Fatal("test job has no pinned actions/checkout@sha — cannot compare")
	}
	gotSHA := firstUsesSHA(job, "actions/checkout")
	if gotSHA == "" {
		t.Fatalf("%s job has no actions/checkout step at all", prKeyGateJob)
	}
	if gotSHA != wantSHA {
		t.Errorf("%s job pins actions/checkout@%s, want the same sha the test job pins (%s)", prKeyGateJob, gotSHA, wantSHA)
	}
}

func TestWorkflow_PRKeyGatePinsSetupGoToTheSameSHAAsTest(t *testing.T) {
	text := readCIWorkflow(t)
	testJob := testJobBlock(t, text)
	job := prKeyGateBlock(t, text)
	wantSHA := firstUsesSHA(testJob, "actions/setup-go")
	if wantSHA == "" {
		t.Fatal("test job has no pinned actions/setup-go@sha — cannot compare")
	}
	gotSHA := firstUsesSHA(job, "actions/setup-go")
	if gotSHA == "" {
		t.Fatalf("%s job has no actions/setup-go step at all", prKeyGateJob)
	}
	if gotSHA != wantSHA {
		t.Errorf("%s job pins actions/setup-go@%s, want the same sha the test job pins (%s)", prKeyGateJob, gotSHA, wantSHA)
	}
}
