// Package scripts_test pins the ci.yml jobs that test the kit and its tools image, publish that image and cut the release tag that pins it with scripts/release-block-kit.sh, and run plain-delivery's commands at a release tag.
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

func checkFields(t *testing.T, workflow, prefix string, cases []struct{ name, got, want string }) {
	t.Helper()
	for _, tc := range cases {
		t.Run(prefix+tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s field = %q, want %q", workflow, tc.got, tc.want)
			}
		})
	}
}

// prDeliveryRun is how ci.yml and publish-block.yml run plain-delivery's delivery check, at one release.
const prDeliveryRun = `go run github.com/mitstack-eu/plain-delivery/cmd/pr-delivery@v0.2.0 --repo . --base "$BASE_SHA" --head "$HEAD_SHA"`

func TestCIWorkflow_TestsGatesReleasesAndRecordsTheKit(t *testing.T) {
	const checkout, setupGo = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
	const releaseDryRun = "bash scripts/release-block-kit.sh --dry-run \\\n--base \"${{ github.event.pull_request.base.sha }}\" \\\n--head \"${{ github.event.pull_request.head.sha }}\""
	const rescueSSH = "capabilities/hetzner/robot/rescuessh"
	const moduleDryRun = "bash scripts/release-block-kit.sh --module " + rescueSSH + " --dry-run \\\n--base \"${{ github.event.pull_request.base.sha }}\" \\\n--head \"${{ github.event.pull_request.head.sha }}\""
	const moduleScan = "go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -C " + rescueSSH + " -scan package ./..."
	const healthz = "helpers/healthz"
	const healthzDryRun = "bash scripts/release-block-kit.sh --module " + healthz + " --dry-run \\\n--base \"${{ github.event.pull_request.base.sha }}\" \\\n--head \"${{ github.event.pull_request.head.sha }}\""
	const planned = "steps.plan.outputs.planned != ''"
	const copyCosign = `bash scripts/copy-tool.sh ghcr.io/sigstore/cosign/cosign:v2.5.3@sha256:f1946d0f30fc8e3777b02f2201e02efdba9fe38f4918162f937052fac98e083f /ko-app/cosign 056df4b41218fff47e879470554e01b0ba8e7608c1bec1e704c9dcded965f00f "$RUNNER_TEMP/bin/cosign"` +
		"\n" + `echo "$RUNNER_TEMP/bin" >> "$GITHUB_PATH"`
	const toolsImageTest = "go test -tags toolsimage -count=1 -run '^TestToolsImage_' ./helpers/tools"
	const cutPinned = `bash scripts/release-block-kit.sh --tools-image "$TOOLS_IMAGE" | tee -a "$GITHUB_STEP_SUMMARY"`
	const buildToolsImage = "docker build --platform linux/amd64 -f helpers/tools/Dockerfile -t block-kit-tools:ci .\n" +
		"TOOLS_IMAGE=block-kit-tools:ci go test -tags toolsimage -count=1 -run '^TestToolsImage_(HoldsEveryFileAtItsPinInALayerOfItsOwn|RunsTheSignInCompanion)$' ./helpers/tools"
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
		{"the test job pins go-version to 1.26.11, whose standard library the rescuessh scan finds clean", strconv.FormatBool(stepWith(all, "test", "with.go-version", "'1.26.11'")), "true"},
		{"the test job runs go vet ./...", strconv.FormatBool(stepWith(all, "test", "run", "go vet ./...")), "true"},
		{"the test job runs go test ./..., and checkpolls against a pull request's base", strconv.FormatBool(stepWith(all, "test", "run", "go test ./...")) + " " + field(stepOf(all, "test", "run", `go run ./cmd/checkpolls -base "$BASE_SHA"`)+"if") + " " + field(stepOf(all, "test", "run", `go run ./cmd/checkpolls -base "$BASE_SHA"`)+"env.BASE_SHA"), "true github.event_name == 'pull_request' ${{ github.event.pull_request.base.sha }}"},
		{"the release job needs the test job", field("jobs.release.needs"), "test"},
		{"the release job runs only on main, by a push or a dispatch, so no unmerged commit is tagged", field("jobs.release.if"),
			"(github.event_name == 'push' || github.event_name == 'workflow_dispatch') && github.ref == 'refs/heads/main'"},
		{"the release job grants contents: write", field("jobs.release.permissions.contents"), "write"},
		{"the release job's concurrency group is kit-release", field("jobs.release.concurrency.group"), "kit-release"},
		{"the release job never cancels a release in progress", field("jobs.release.concurrency.cancel-in-progress"), "false"},
		{"the release job pins checkout to the platform's sha", strconv.FormatBool(stepWith(all, "release", "uses", checkout)), "true"},
		{"the release job checks out the tags the version scan reads", strconv.FormatBool(stepWith(all, "release", "with.fetch-depth", "0")), "true"},
		{"the release job runs its script under bash, so pipefail fails a red release", strconv.FormatBool(stepWith(all, "release", "shell", "bash")), "true"},
		{"the release job plans its tag with the release script's dry run into the job summary", field(stepOf(all, "release", "run", `bash scripts/release-block-kit.sh --dry-run | tee -a "$GITHUB_STEP_SUMMARY"`) + "id"), "plan"},
		{"the release job pushes the tools image only when a tag is planned", field(stepOf(all, "release", "id", "image") + "if"), planned},
		{"the release job grants packages: write to push the tools image", field("jobs.release.permissions.packages"), "write"},
		{"the release job grants id-token: write to sign the tools image keyless", field("jobs.release.permissions.id-token"), "write"},
		{"the release job names the tools image's package", field("jobs.release.env.TOOLS_PACKAGE"), "ghcr.io/mitstack-eu/block-kit-tools"},
		{"the release job copies cosign out of its publisher's image by digest", strconv.FormatBool(stepWith(all, "release", "run", copyCosign)), "true"},
		{"the release job copies every tool out of the image it pushed, through Docker and over the registry's HTTP API",
			field(stepOf(all, "release", "run", toolsImageTest) + "env.TOOLS_IMAGE"), "${{ steps.image.outputs.ref }}"},
		{"the release job cuts the tag on a commit that pins the image it pushed", field(stepOf(all, "release", "run", cutPinned) + "env.TOOLS_IMAGE"), "${{ steps.image.outputs.ref }}"},
		{"the pinned cut runs under bash, so pipefail fails a red release", field(stepOf(all, "release", "run", cutPinned) + "shell"), "bash"},
		{"the release job pulls the image with no login, then copies its tools, then cuts the tag", strconv.FormatBool(
			stepAt(all, "release", "name", "Verify the signature and pull the image with no login") < stepAt(all, "release", "run", toolsImageTest) &&
				stepAt(all, "release", "run", toolsImageTest) < stepAt(all, "release", "run", cutPinned)), "true"},
		{"the test job builds the tools image, checks every file it holds against its pin, and runs the sign-in route in caddy:2", strconv.FormatBool(stepWith(all, "test", "run", buildToolsImage) && stepWith(all, "test", "run", "CADDY_IMAGE=caddy:2 go test -tags edgecaddy -count=1 -run '^TestEdge_' ./helpers/dockerhost")), "true"},
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
		{"the delivery check reports as block / delivery, the context every block's main requires", field("jobs.delivery.name"), "block / delivery"},
		{"the delivery check runs only on a pull request, never on a push", field("jobs.delivery.if"), "github.event_name == 'pull_request'"},
		{"the delivery check checks out the full history the commit range needs", strconv.FormatBool(stepWith(all, "delivery", "with.fetch-depth", "0")), "true"},
		{"the delivery check pins checkout to the platform's sha", strconv.FormatBool(stepWith(all, "delivery", "uses", checkout)), "true"},
		{"the delivery check pins setup-go to the platform's sha", strconv.FormatBool(stepWith(all, "delivery", "uses", setupGo)), "true"},
		{"the delivery check runs plain-delivery's pr-delivery at its release on the pull request's base and head", field(stepOf(all, "delivery", "run", prDeliveryRun)+"env.BASE_SHA") + " " + field(stepOf(all, "delivery", "run", prDeliveryRun)+"env.HEAD_SHA"),
			"${{ github.event.pull_request.base.sha }} ${{ github.event.pull_request.head.sha }}"},
		{"the delivery check reads the title and the author's login from the pull request event", field(stepOf(all, "delivery", "run", prDeliveryRun)+"env.PR_TITLE") + " " + field(stepOf(all, "delivery", "run", prDeliveryRun)+"env.PR_AUTHOR"),
			"${{ github.event.pull_request.title }} ${{ github.event.pull_request.user.login }}"},
		{"the delivery check reads the board with the recorder's address and token", field(stepOf(all, "delivery", "run", prDeliveryRun)+"env.TRACKER_BASE_URL") + " " + field(stepOf(all, "delivery", "run", prDeliveryRun)+"env.TRACKER_API_TOKEN"),
			"${{ vars.TRACKER_BASE_URL }} ${{ secrets.TRACKER_API_TOKEN }}"},
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
	checkFields(t, "ci.yml", "", cases)
	for _, tc := range []struct{ name, scope, forbidden string }{
		{"the test job never continues on error, so a failing step fails the check main requires", "jobs.test.", "continue-on-error"},
		{"the delivery check never continues on error, so code before its tests is a red check", "jobs.delivery.", "continue-on-error"},
		{"the delivery check reads nothing from a commit, since a branch name in a commit is no delivery intent", "jobs.delivery.", "head_commit"},
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
