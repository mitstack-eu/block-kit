// Package scripts_test pins the ci.yml jobs that test the kit, cut its release tag with scripts/release-block-kit.sh, and run the board module's commands.
package scripts_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// workflowFields returns every key in workflow under its dotted path, such as on.push.branches, with a block scalar's lines joined and a mapping key's value empty.
func workflowFields(workflow string) map[string]string {
	type level struct {
		indent int
		path   string
	}
	fields := map[string]string{}
	items := map[string]int{}
	var stack []level
	block, blockIndent := "", -1
	keyLine := regexp.MustCompile(`^([\w.-]+):\s*(.*)$`)
	for _, line := range strings.Split(workflow, "\n") {
		text := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if text != "" && blockIndent >= 0 && indent > blockIndent {
			fields[block] = strings.TrimPrefix(fields[block]+"\n"+text, "\n")
			continue
		}
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		blockIndent = -1
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		parent := ""
		if len(stack) > 0 {
			parent = stack[len(stack)-1].path
		}
		if rest, ok := strings.CutPrefix(text, "- "); ok {
			n := items[parent]
			items[parent] = n + 1
			parent += "." + strconv.Itoa(n)
			fields[parent] = ""
			stack = append(stack, level{indent, parent})
			text, indent = rest, indent+2
		}
		m := keyLine.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		path := strings.TrimPrefix(parent+"."+m[1], ".")
		value, _, _ := strings.Cut(m[2], " #")
		switch value = strings.TrimSpace(value); value {
		case "":
			fields[path] = ""
			stack = append(stack, level{indent, path})
		case "|", ">", "|-", ">-":
			block, blockIndent = path, indent
		default:
			fields[path] = value
		}
	}
	return fields
}

// stepWith reports whether one of job's steps sets key to value.
func stepWith(all map[string]string, job, key, value string) bool {
	for i := 0; ; i++ {
		prefix := "jobs." + job + ".steps." + strconv.Itoa(i) + "."
		if _, ok := all[strings.TrimSuffix(prefix, ".")]; !ok {
			return false
		}
		if v, ok := all[prefix+key]; ok && v == value {
			return true
		}
	}
}

func TestCIWorkflow_TestsGatesReleasesAndRecordsTheKit(t *testing.T) {
	const checkout, setupGo = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
	raw, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatalf("reading ci.yml: %v", err)
	}
	all := workflowFields(string(raw))
	field := func(path string) string {
		v, ok := all[path]
		if !ok {
			return "<absent>"
		}
		return v
	}
	cases := []struct{ name, got, want string }{
		{"it runs on a pull request", field("on.pull_request"), ""},
		{"it runs on a push to main", field("on.push.branches"), "[main]"},
		{"it runs on a dispatch", field("on.workflow_dispatch"), ""},
		{"the test job runs on vars.CI_RUNS_ON with the ubicloud-standard-2 fallback", field("jobs.test.runs-on"), `${{ fromJSON(vars.CI_RUNS_ON || '["ubicloud-standard-2"]') }}`},
		{"the test job pins checkout to the platform's sha", strconv.FormatBool(stepWith(all, "test", "uses", checkout)), "true"},
		{"the test job pins setup-go to the platform's sha", strconv.FormatBool(stepWith(all, "test", "uses", setupGo)), "true"},
		{"the test job pins go-version to 1.26.8", strconv.FormatBool(stepWith(all, "test", "with.go-version", "'1.26.8'")), "true"},
		{"the test job runs go vet ./...", strconv.FormatBool(stepWith(all, "test", "run", "go vet ./...")), "true"},
		{"the test job runs go test ./...", strconv.FormatBool(stepWith(all, "test", "run", "go test ./...")), "true"},
		{"the release job needs the test job", field("jobs.release.needs"), "test"},
		{"the release job runs only on main, by a push or a dispatch, so no unmerged commit is tagged", field("jobs.release.if"),
			"(github.event_name == 'push' || github.event_name == 'workflow_dispatch') && github.ref == 'refs/heads/main'"},
		{"the release job grants contents: write", field("jobs.release.permissions.contents"), "write"},
		{"the release job's concurrency group is kit-release", field("jobs.release.concurrency.group"), "kit-release"},
		{"the release job never cancels a release in progress", field("jobs.release.concurrency.cancel-in-progress"), "false"},
		{"the release job pins checkout to the platform's sha", strconv.FormatBool(stepWith(all, "release", "uses", checkout)), "true"},
		{"the release job checks out the tags the version scan reads", strconv.FormatBool(stepWith(all, "release", "with.fetch-depth", "0")), "true"},
		{"the release job runs its script under bash, so pipefail fails a red release", strconv.FormatBool(stepWith(all, "release", "shell", "bash")), "true"},
		{"the release job runs the release script into the job summary", strconv.FormatBool(stepWith(all, "release", "run", `bash scripts/release-block-kit.sh | tee -a "$GITHUB_STEP_SUMMARY"`)), "true"},
		{"the test job vets the board module", strconv.FormatBool(stepWith(all, "test", "run", "go -C board vet ./...")), "true"},
		{"the test job tests the board module", strconv.FormatBool(stepWith(all, "test", "run", "go -C board test ./...")), "true"},
		{"the pr key gate runs only on a pull request, never on a push", field("jobs.pr-key-gate.if"), "github.event_name == 'pull_request'"},
		{"the pr key gate checks out the full history the base..head range needs", strconv.FormatBool(stepWith(all, "pr-key-gate", "with.fetch-depth", "0")), "true"},
		{"the pr key gate pins checkout to the platform's sha", strconv.FormatBool(stepWith(all, "pr-key-gate", "uses", checkout)), "true"},
		{"the pr key gate pins setup-go to the platform's sha", strconv.FormatBool(stepWith(all, "pr-key-gate", "uses", setupGo)), "true"},
		{"the pr key gate reads the title from the pull request event, never from a commit", strconv.FormatBool(stepWith(all, "pr-key-gate", "env.PR_TITLE", "${{ github.event.pull_request.title }}")), "true"},
		{"the pr key gate runs the board module's pr-keys on the pull request's base and head", strconv.FormatBool(stepWith(all, "pr-key-gate", "run",
			"go -C board run ./cmd/pr-keys \\\n--repo \"$GITHUB_WORKSPACE\" \\\n--base \"${{ github.event.pull_request.base.sha }}\" \\\n--head \"${{ github.event.pull_request.head.sha }}\"")), "true"},
		{"record delivery runs only on main, by a push or a dispatch, so no other ref records keys main never took", field("jobs.record-delivery.if"),
			"(github.event_name == 'push' || github.event_name == 'workflow_dispatch') && github.ref == 'refs/heads/main'"},
		{"record delivery needs a green test suite and the release job to have run", field("jobs.record-delivery.needs"), "[test, release]"},
		{"record delivery checks out the full history and tags range resolution reads", strconv.FormatBool(stepWith(all, "record-delivery", "with.fetch-depth", "0")), "true"},
		{"record delivery pins checkout to the platform's sha", strconv.FormatBool(stepWith(all, "record-delivery", "uses", checkout)), "true"},
		{"record delivery pins setup-go to the platform's sha", strconv.FormatBool(stepWith(all, "record-delivery", "uses", setupGo)), "true"},
		{"record delivery passes the tracker URL from vars", strconv.FormatBool(stepWith(all, "record-delivery", "env.TRACKER_BASE_URL", "${{ vars.TRACKER_BASE_URL }}")), "true"},
		{"record delivery passes the tracker token from secrets", strconv.FormatBool(stepWith(all, "record-delivery", "env.TRACKER_API_TOKEN", "${{ secrets.TRACKER_API_TOKEN }}")), "true"},
		{"record delivery runs the board module's recorder on the push's before sha and the current sha", strconv.FormatBool(stepWith(all, "record-delivery", "run",
			"go -C board run ./cmd/board-delivery \\\n--repo \"$GITHUB_WORKSPACE\" \\\n--before \"${{ github.event.before }}\" \\\n--after \"${{ github.sha }}\"")), "true"},
		{"record delivery grants contents: write to advance the board-synced tag", field("jobs.record-delivery.permissions.contents"), "write"},
		{"record delivery serialises its runs in one concurrency group", field("jobs.record-delivery.concurrency.group"), "record-delivery"},
		{"record delivery never cancels a run between the board write and the tag advance", field("jobs.record-delivery.concurrency.cancel-in-progress"), "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("ci.yml field = %q, want %q", tc.got, tc.want)
			}
		})
	}
	for _, tc := range []struct{ name, scope, forbidden string }{
		{"the pr key gate never continues on error, so a missing key is a red check", "jobs.pr-key-gate.", "continue-on-error"},
		{"the pr key gate needs no secret", "jobs.pr-key-gate.", "secrets."},
		{"the pr key gate reads nothing from a commit, since a branch name in a commit is no delivery intent", "jobs.pr-key-gate.", "head_commit"},
		{"record delivery never continues on error, so a failed board write is a red run", "jobs.record-delivery.", "continue-on-error"},
		{"record delivery never reads release's outputs.released, so a push that cut no tag still delivers its keys", "jobs.record-delivery.", "outputs.released"},
		{"no workflow value derives keys itself instead of calling boardkeys", "", "MIT-[0-9]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for path, v := range all {
				if strings.HasPrefix(path, tc.scope) && strings.Contains(path+": "+v, tc.forbidden) {
					t.Errorf("ci.yml %s = %q names %s", path, v, tc.forbidden)
				}
			}
		})
	}
}
