package main

// The record-delivery job in ci.yml is read by indentation, since the kit takes no YAML library.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const ciWorkflowPath = "../../.github/workflows/ci.yml"

const recordDeliveryJob = "record-delivery"

func ciIndentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// ciBlock returns the lines nested under "<key>:" inside text.
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

func recordDeliveryBlock(t *testing.T, text string) string {
	t.Helper()
	job := ciBlock(ciBlock(text, "jobs"), recordDeliveryJob)
	if job == "" {
		t.Fatalf("no %q job found under jobs: in %s", recordDeliveryJob, ciWorkflowPath)
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

func TestWorkflow_RecordDeliveryJobExists(t *testing.T) {
	text := readCIWorkflow(t)
	recordDeliveryBlock(t, text)
}

// A dispatch on another ref would record keys for commits main never took.
func TestWorkflow_RecordDeliveryRunsOnlyOnMainByPushOrDispatch(t *testing.T) {
	const want = "(github.event_name == 'push' || github.event_name == 'workflow_dispatch') && github.ref == 'refs/heads/main'"
	text := readCIWorkflow(t)

	m := regexp.MustCompile(`(?m)^\s*if:\s*(.+)$`).FindStringSubmatch(recordDeliveryBlock(t, text))

	if m == nil {
		t.Fatalf("%s job has no if: gate; want %q", recordDeliveryJob, want)
	}
	if got := strings.TrimSpace(m[1]); got != want {
		t.Errorf("%s job's if: = %q; want %q", recordDeliveryJob, got, want)
	}
}

func TestWorkflow_RecordDeliveryNeedsTestAndRelease(t *testing.T) {
	text := readCIWorkflow(t)
	job := recordDeliveryBlock(t, text)
	needsPattern := regexp.MustCompile(`(?m)^\s*needs:\s*(.+)$`)
	m := needsPattern.FindStringSubmatch(job)
	if m == nil {
		t.Fatalf("%s job has no needs: key", recordDeliveryJob)
	}
	needs := strings.Trim(strings.TrimSpace(m[1]), "[]")
	if !strings.Contains(needs, "test") {
		t.Errorf("%s job's needs = %q, want it to name test — a red test suite must never record a delivery", recordDeliveryJob, needs)
	}
	if !strings.Contains(needs, "release") {
		t.Errorf("%s job's needs = %q, want it to name release — a change unreachable to consumers must not be claimed delivered before the release job has run", recordDeliveryJob, needs)
	}
}

// A push that cuts no tag still delivers its keys, so the job must not read release's outputs.
func TestWorkflow_RecordDeliveryDoesNotGateOnReleaseHavingCutATag(t *testing.T) {
	text := readCIWorkflow(t)
	job := recordDeliveryBlock(t, text)
	if strings.Contains(job, "outputs.released") {
		t.Errorf("%s job references release's outputs.released — it must record a docs-only push's keys too, even when release cut no tag:\n%s", recordDeliveryJob, job)
	}
}

func TestWorkflow_RecordDeliveryChecksOutFullHistory(t *testing.T) {
	text := readCIWorkflow(t)
	job := recordDeliveryBlock(t, text)
	if !strings.Contains(job, "fetch-depth: 0") {
		t.Errorf("%s job must check out with fetch-depth: 0 — range resolution reads tags and history a shallow checkout has neither", recordDeliveryJob)
	}
}

func firstUsesSHA(text, action string) string {
	pattern := regexp.MustCompile(`uses:\s*` + regexp.QuoteMeta(action) + `@(\S+)`)
	m := pattern.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestWorkflow_RecordDeliveryPinsCheckoutToTheSameSHAAsTest(t *testing.T) {
	text := readCIWorkflow(t)
	testJob := testJobBlock(t, text)
	job := recordDeliveryBlock(t, text)
	wantSHA := firstUsesSHA(testJob, "actions/checkout")
	if wantSHA == "" {
		t.Fatal("test job has no pinned actions/checkout@sha — cannot compare")
	}
	gotSHA := firstUsesSHA(job, "actions/checkout")
	if gotSHA == "" {
		t.Fatalf("%s job never uses actions/checkout", recordDeliveryJob)
	}
	if gotSHA != wantSHA {
		t.Errorf("%s job pins actions/checkout@%s, want the same sha the test job pins (%s)", recordDeliveryJob, gotSHA, wantSHA)
	}
}

func TestWorkflow_RecordDeliveryPinsSetupGoToTheSameSHAAsTest(t *testing.T) {
	text := readCIWorkflow(t)
	testJob := testJobBlock(t, text)
	job := recordDeliveryBlock(t, text)
	wantSHA := firstUsesSHA(testJob, "actions/setup-go")
	if wantSHA == "" {
		t.Fatal("test job has no pinned actions/setup-go@sha — cannot compare")
	}
	gotSHA := firstUsesSHA(job, "actions/setup-go")
	if gotSHA == "" {
		t.Fatalf("%s job never uses actions/setup-go", recordDeliveryJob)
	}
	if gotSHA != wantSHA {
		t.Errorf("%s job pins actions/setup-go@%s, want the same sha the test job pins (%s)", recordDeliveryJob, gotSHA, wantSHA)
	}
}

func TestWorkflow_RecordDeliveryPassesTrackerBaseURLAndTokenFromVarsAndSecrets(t *testing.T) {
	text := readCIWorkflow(t)
	job := recordDeliveryBlock(t, text)
	if !strings.Contains(job, "vars.TRACKER_BASE_URL") {
		t.Errorf("%s job must pass TRACKER_BASE_URL from vars, job block:\n%s", recordDeliveryJob, job)
	}
	if !strings.Contains(job, "secrets.TRACKER_API_TOKEN") {
		t.Errorf("%s job must pass TRACKER_API_TOKEN from secrets, job block:\n%s", recordDeliveryJob, job)
	}
}

func TestWorkflow_RecordDeliveryPassesBeforeAndCurrentSHAToTheRecorder(t *testing.T) {
	text := readCIWorkflow(t)
	job := recordDeliveryBlock(t, text)
	if !strings.Contains(job, "github.event.before") {
		t.Errorf("%s job must pass the push event's before sha into the recorder, job block:\n%s", recordDeliveryJob, job)
	}
	if !strings.Contains(job, "github.sha") {
		t.Errorf("%s job must pass the current sha into the recorder, job block:\n%s", recordDeliveryJob, job)
	}
}

func TestWorkflow_RecordDeliveryInvokesTheBoardDeliveryCommand(t *testing.T) {
	text := readCIWorkflow(t)
	job := recordDeliveryBlock(t, text)
	if !strings.Contains(job, "cmd/board-delivery") {
		t.Errorf("%s job must invoke the cmd/board-delivery command, job block:\n%s", recordDeliveryJob, job)
	}
}

func TestWorkflow_NoInlineKeyDerivationRegexOutsideTheRecorder(t *testing.T) {
	text := readCIWorkflow(t)
	if strings.Contains(text, "MIT-[0-9]") {
		t.Error("the workflow inlines a MIT-[0-9] key-derivation regex — key derivation lives in exactly one place, the boardkeys package the recorder calls, never re-implemented in YAML")
	}
}

func TestWorkflow_RecordDeliveryGrantsContentsWrite(t *testing.T) {
	text := readCIWorkflow(t)
	job := recordDeliveryBlock(t, text)
	permissions := ciBlock(job, "permissions")
	if !strings.Contains(permissions, "contents: write") {
		t.Errorf("%s job must grant contents: write — it advances the board-synced git tag that anchors the next run's range, job block:\n%s", recordDeliveryJob, job)
	}
}

func TestWorkflow_RecordDeliveryHasNoContinueOnError(t *testing.T) {
	text := readCIWorkflow(t)
	job := recordDeliveryBlock(t, text)
	if strings.Contains(job, "continue-on-error") {
		t.Errorf("%s job must not carry continue-on-error — a failed board write must be a red run, not a silent one", recordDeliveryJob)
	}
}

// Overlapping runs would race the anchor tag, and a run cancelled between the
// board write and the tag advance leaves the anchor further behind.
func TestWorkflow_RecordDeliveryHasConcurrencyGroupThatDoesNotCancel(t *testing.T) {
	text := readCIWorkflow(t)
	job := recordDeliveryBlock(t, text)
	concurrency := ciBlock(job, "concurrency")
	if concurrency == "" {
		t.Fatalf("%s job has no concurrency: block — two overlapping pushes to main can race the board-synced anchor tag, job block:\n%s", recordDeliveryJob, job)
	}
	if !strings.Contains(concurrency, "cancel-in-progress: false") {
		t.Errorf("%s job's concurrency block must set cancel-in-progress: false — cancelling a run that already posted to the board but has not yet advanced the anchor would leave the anchor behind its real position, concurrency block:\n%s", recordDeliveryJob, concurrency)
	}
}
