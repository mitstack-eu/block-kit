// Package scripts_test pins the ci.yml jobs that test the kit, cut its release tag with scripts/release-block-kit.sh, and run plain-delivery's commands at a release tag.
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

func stepOf(all map[string]string, job, key, value string) string {
	for i := 0; ; i++ {
		prefix := "jobs." + job + ".steps." + strconv.Itoa(i) + "."
		if _, ok := all[strings.TrimSuffix(prefix, ".")]; !ok {
			return ""
		}
		if v, ok := all[prefix+key]; ok && v == value {
			return prefix
		}
	}
}

func stepWith(all map[string]string, job, key, value string) bool {
	return stepOf(all, job, key, value) != ""
}

func TestCIWorkflow_TestsGatesReleasesAndRecordsTheKit(t *testing.T) {
	const checkout, setupGo = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
	const releaseDryRun = "bash scripts/release-block-kit.sh --dry-run \\\n--base \"${{ github.event.pull_request.base.sha }}\" \\\n--head \"${{ github.event.pull_request.head.sha }}\""
	const rescueSSH = "capabilities/hetzner/robot/rescuessh"
	const moduleDryRun = "bash scripts/release-block-kit.sh --module " + rescueSSH + " --dry-run \\\n--base \"${{ github.event.pull_request.base.sha }}\" \\\n--head \"${{ github.event.pull_request.head.sha }}\""
	const moduleScan = "go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -C " + rescueSSH + " -scan package ./..."
	const healthz = "helpers/healthz"
	const healthzDryRun = "bash scripts/release-block-kit.sh --module " + healthz + " --dry-run \\\n--base \"${{ github.event.pull_request.base.sha }}\" \\\n--head \"${{ github.event.pull_request.head.sha }}\""
	raw, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatalf("reading ci.yml: %v", err)
	}
	all := workflowFields(string(raw))
	field := func(path string) string {
		if v, ok := all[path]; ok {
			return v
		}
		return "<absent>"
	}
	cases := []struct{ name, got, want string }{
		{"it runs on a pull request", field("on.pull_request"), ""},
		{"it runs on a push to main", field("on.push.branches"), "[main]"},
		{"it runs on a dispatch", field("on.workflow_dispatch"), ""},
		{"the test job runs on vars.CI_RUNS_ON with the ubicloud-standard-2 fallback", field("jobs.test.runs-on"), `${{ fromJSON(vars.CI_RUNS_ON || '["ubicloud-standard-2"]') }}`},
		{"the test job pins checkout to the platform's sha", strconv.FormatBool(stepWith(all, "test", "uses", checkout)), "true"},
		{"the test job pins setup-go to the platform's sha", strconv.FormatBool(stepWith(all, "test", "uses", setupGo)), "true"},
		{"the test job pins go-version to 1.26.10, whose standard library the rescuessh scan finds clean", strconv.FormatBool(stepWith(all, "test", "with.go-version", "'1.26.10'")), "true"},
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
		{"the test job vets and tests the example block, then runs blockverify on it", strconv.FormatBool(stepWith(all, "test", "run", "go -C examples/hetzner-firewall vet ./...\ngo -C examples/hetzner-firewall test ./...\ngo run ./verify/blockverify --standalone examples/hetzner-firewall")), "true"},
		{"the test job vets and tests the rescuessh module, which ./... leaves out", strconv.FormatBool(stepWith(all, "test", "run", "go -C "+rescueSSH+" vet ./...\ngo -C "+rescueSSH+" test ./...")), "true"},
		{"the test job scans the packages the rescuessh module imports for known vulnerabilities", strconv.FormatBool(stepWith(all, "test", "run", moduleScan)), "true"},
		{"the rescuessh scan carries no if, so a push to main runs it too", field(stepOf(all, "test", "run", moduleScan) + "if"), "<absent>"},
		{"the test job dry-runs the rescuessh release over a pull request's base and head, on a pull request alone",
			field(stepOf(all, "test", "run", moduleDryRun) + "if"), "github.event_name == 'pull_request'"},
		{"the release job releases the rescuessh module by its own tags into the job summary", field(stepOf(all, "release", "run", `bash scripts/release-block-kit.sh --module `+rescueSSH+` | tee -a "$GITHUB_STEP_SUMMARY"`) + "shell"), "bash"},
		{"the test job vets and tests the healthz module, which ./... leaves out", strconv.FormatBool(stepWith(all, "test", "run", "go -C "+healthz+" vet ./...\ngo -C "+healthz+" test ./...")), "true"},
		{"the test job dry-runs the healthz release over a pull request's base and head, on a pull request alone",
			field(stepOf(all, "test", "run", healthzDryRun) + "if"), "github.event_name == 'pull_request'"},
		{"the release job releases the healthz module by its own tags into the job summary", field(stepOf(all, "release", "run", `bash scripts/release-block-kit.sh --module `+healthz+` | tee -a "$GITHUB_STEP_SUMMARY"`) + "shell"), "bash"},
		{"the test job reports as test, the context branch protection requires", field("jobs.test.name"), "test"},
		{"the test job carries no if, so every event runs the check main requires", field("jobs.test.if"), "<absent>"},
		{"the release tests carry no if, so every event runs them", field(stepOf(all, "test", "run", "bash scripts/release-block-kit_test.sh") + "if"), "<absent>"},
		{"the test job checks out the tags and history the release dry run reads", strconv.FormatBool(stepWith(all, "test", "with.fetch-depth", "0")), "true"},
		{"the test job runs the release script's tests", strconv.FormatBool(stepWith(all, "test", "run", "bash scripts/release-block-kit_test.sh")), "true"},
		{"the test job dry-runs the release over a pull request's base and head, on a pull request alone",
			field(stepOf(all, "test", "run", releaseDryRun) + "if"), "github.event_name == 'pull_request'"},
		{"the pr key gate runs only on a pull request, never on a push", field("jobs.pr-key-gate.if"), "github.event_name == 'pull_request'"},
		{"the pr key gate checks out the full history the base..head range needs", strconv.FormatBool(stepWith(all, "pr-key-gate", "with.fetch-depth", "0")), "true"},
		{"the pr key gate pins checkout to the platform's sha", strconv.FormatBool(stepWith(all, "pr-key-gate", "uses", checkout)), "true"},
		{"the pr key gate pins setup-go to the platform's sha", strconv.FormatBool(stepWith(all, "pr-key-gate", "uses", setupGo)), "true"},
		{"the pr key gate reads the title from the pull request event, never from a commit", strconv.FormatBool(stepWith(all, "pr-key-gate", "env.PR_TITLE", "${{ github.event.pull_request.title }}")), "true"},
		{"the pr key gate runs plain-delivery's pr-keys at v0.1.0 on the pull request's base and head", strconv.FormatBool(stepWith(all, "pr-key-gate", "run",
			"go run github.com/mitstack-eu/plain-delivery/cmd/pr-keys@v0.1.0 \\\n--repo . \\\n--base \"${{ github.event.pull_request.base.sha }}\" \\\n--head \"${{ github.event.pull_request.head.sha }}\"")), "true"},
		{"record delivery runs only on main, by a push or a dispatch, so no other ref records keys main never took", field("jobs.record-delivery.if"),
			"(github.event_name == 'push' || github.event_name == 'workflow_dispatch') && github.ref == 'refs/heads/main'"},
		{"record delivery needs a green test suite and the release job to have run", field("jobs.record-delivery.needs"), "[test, release]"},
		{"record delivery checks out the full history and tags range resolution reads", strconv.FormatBool(stepWith(all, "record-delivery", "with.fetch-depth", "0")), "true"},
		{"record delivery pins checkout to the platform's sha", strconv.FormatBool(stepWith(all, "record-delivery", "uses", checkout)), "true"},
		{"record delivery pins setup-go to the platform's sha", strconv.FormatBool(stepWith(all, "record-delivery", "uses", setupGo)), "true"},
		{"record delivery passes the tracker URL from vars", strconv.FormatBool(stepWith(all, "record-delivery", "env.TRACKER_BASE_URL", "${{ vars.TRACKER_BASE_URL }}")), "true"},
		{"record delivery passes the tracker token from secrets", strconv.FormatBool(stepWith(all, "record-delivery", "env.TRACKER_API_TOKEN", "${{ secrets.TRACKER_API_TOKEN }}")), "true"},
		{"record delivery runs plain-delivery's recorder at v0.1.0 on the push's before sha and the current sha", strconv.FormatBool(stepWith(all, "record-delivery", "run",
			"go run github.com/mitstack-eu/plain-delivery/cmd/board-delivery@v0.1.0 \\\n--repo . \\\n--before \"${{ github.event.before }}\" \\\n--after \"${{ github.sha }}\"")), "true"},
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
		{"the test job never continues on error, so a failing step fails the check main requires", "jobs.test.", "continue-on-error"},
		{"the pr key gate never continues on error, so a missing key is a red check", "jobs.pr-key-gate.", "continue-on-error"},
		{"the pr key gate needs no secret", "jobs.pr-key-gate.", "secrets."},
		{"the pr key gate reads nothing from a commit, since a branch name in a commit is no delivery intent", "jobs.pr-key-gate.", "head_commit"},
		{"record delivery never continues on error, so a failed board write is a red run", "jobs.record-delivery.", "continue-on-error"},
		{"record delivery never reads release's outputs.released, so a push that cut no tag still delivers its keys", "jobs.record-delivery.", "outputs.released"},
		{"no workflow value derives keys itself instead of calling plain-delivery", "", "MIT-[0-9]"},
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
