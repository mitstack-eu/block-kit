package main

// This guard is a Go test because nothing runs the kit's shell guards, and it reads the workflow by field because the kit is stdlib-only.

import (
	"cmp"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mitstack-eu/block-kit/verify/kittest"
)

const workflowPath = "../../.github/workflows/publish-block.yml"

// workflowFields returns every key in workflow under its dotted path, such as jobs.publish.steps.3.run, with a block scalar's lines joined and a mapping key's value empty.
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

func readWorkflow(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("reading %s: %v", workflowPath, err)
	}
	return raw
}

func allFields(t *testing.T) map[string]string {
	t.Helper()
	return workflowFields(string(readWorkflow(t)))
}

type step map[string]string

func jobSteps(t *testing.T, all map[string]string, job string) []step {
	t.Helper()
	var steps []step
	for i := 0; ; i++ {
		prefix := "jobs." + job + ".steps." + strconv.Itoa(i)
		if _, ok := all[prefix]; !ok {
			break
		}
		s := step{}
		for k, v := range all {
			if rest, ok := strings.CutPrefix(k, prefix+"."); ok {
				s[rest] = v
			}
		}
		steps = append(steps, s)
	}
	if len(steps) == 0 {
		t.Fatalf("job %q has no steps in %s", job, workflowPath)
	}
	return steps
}

func (s step) label() string {
	if n, ok := s["name"]; ok {
		return n
	}
	action, _, _ := strings.Cut(s["uses"], "@")
	return action
}

func labels(steps []step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.label()
	}
	return out
}

func indexOf(steps []step, label string) int {
	return slices.Index(labels(steps), label)
}

func indexOfID(steps []step, id string) int {
	return slices.IndexFunc(steps, func(s step) bool { return s["id"] == id })
}

func ifOf(expr string) string {
	expr = strings.TrimSpace(expr)
	expr = strings.TrimSuffix(strings.TrimPrefix(expr, "${{"), "}}")
	return strings.TrimSpace(expr)
}

func publishSteps(t *testing.T) []step {
	t.Helper()
	return jobSteps(t, allFields(t), "publish")
}

func script(t *testing.T, job, name string) kittest.WorkflowScript {
	t.Helper()
	s, err := kittest.ReadWorkflowScript(readWorkflow(t), job, name)
	if err != nil {
		t.Fatalf("reading the %q step of %s: %v", name, job, err)
	}
	return s
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

type scriptResult struct {
	stdout, stderr, combined string
	exitCode                 int
}

// lockedBuilder serialises writes, since exec copies stdout and stderr from two goroutines into the one combined buffer.
type lockedBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuilder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuilder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func runBash(t *testing.T, scriptPath, dir string, env ...string) scriptResult {
	t.Helper()
	cmd := exec.Command("bash", "--noprofile", "--norc", "-e", "-o", "pipefail", scriptPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr strings.Builder
	combined := &lockedBuilder{}
	cmd.Stdout = io.MultiWriter(&stdout, combined)
	cmd.Stderr = io.MultiWriter(&stderr, combined)
	exitCode := 0
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %s: %v", scriptPath, err)
		}
		exitCode = exitErr.ExitCode()
	}
	return scriptResult{stdout: stdout.String(), stderr: stderr.String(), combined: combined.String(), exitCode: exitCode}
}

// keyValues lets a later line for the same key win, as it does on the runner.
func keyValues(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = v
		}
	}
	return out
}

var (
	testDigest       = "sha256:" + strings.Repeat("ab", 32)
	testRecordDigest = "sha256:" + strings.Repeat("cd", 32) // differs from the image's digest so a test can tell the two apart
	testRecordTag    = "sha256-" + strings.Repeat("ab", 32) + ".prov"
	testSha          = strings.Repeat("e", 40)
)

func TestWorkflowFields_ReadsNestedKeysAndSequenceItemsByIndentation(t *testing.T) {
	sample := "jobs:\n  conformance:\n    needs: []\n    steps:\n      - run: go vet ./...\n  publish:\n    needs: conformance # a comment\n    steps:\n      - name: push\n        run: |\n          docker push\n          echo done\n"

	got := workflowFields(sample)

	want := map[string]string{
		"jobs": "", "jobs.conformance": "", "jobs.conformance.needs": "[]", "jobs.conformance.steps": "",
		"jobs.conformance.steps.0": "", "jobs.conformance.steps.0.run": "go vet ./...",
		"jobs.publish": "", "jobs.publish.needs": "conformance", "jobs.publish.steps": "",
		"jobs.publish.steps.0": "", "jobs.publish.steps.0.name": "push", "jobs.publish.steps.0.run": "docker push\necho done",
	}
	if len(got) != len(want) {
		t.Errorf("workflowFields = %q, want %q", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("workflowFields[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestWorkflow_Structure(t *testing.T) {
	all := allFields(t)
	conformance, publish := jobSteps(t, all, "conformance"), jobSteps(t, all, "publish")
	field := func(path string) string {
		if v, ok := all[path]; ok {
			return v
		}
		return "<absent>"
	}
	of := func(steps []step, label, key string) string {
		if i := indexOf(steps, label); i >= 0 {
			if v, ok := steps[i][key]; ok {
				return v
			}
		}
		return "<absent>"
	}
	cases := []struct{ name, got, want string }{
		{"it is a reusable workflow", field("on.workflow_call"), ""},
		{"it has a conformance job", field("jobs.conformance"), ""},
		{"it has a publish job", field("jobs.publish"), ""},
		{"publish needs both conformance jobs and both scans, so a red check never starts it", field("jobs.publish.needs"), "[conformance, opentofu-conformance, secrets, scripts]"},
		{"publish runs only for a caller naming its registry, on a push to the default branch once no check failed, so a pull request gets conformance alone", ifOf(field("jobs.publish.if")),
			"!cancelled() && !failure() && inputs.ecr-repository != '' && github.event_name == 'push' && github.ref == format('refs/heads/{0}', github.event.repository.default_branch)"},
		{"the Go conformance job runs on every event unless the block is OpenTofu-only", ifOf(field("jobs.conformance.if")), "!inputs.opentofu-only"},
		{"the OpenTofu conformance job runs for an OpenTofu-only block alone, and after a failed base image job too, so its check reads failed and never skipped", ifOf(field("jobs.opentofu-conformance.if")), "!cancelled() && inputs.opentofu-only"},
		{"the OpenTofu conformance job waits for the base image", field("jobs.opentofu-conformance.needs"), "base-image"},
		{"the base image job runs for an OpenTofu-only block alone", ifOf(field("jobs.base-image.if")), "inputs.opentofu-only"},
		{"whichever conformance job runs reports as conformance", field("jobs.conformance.name") + " | " + field("jobs.opentofu-conformance.name"),
			"${{ inputs.opentofu-only && 'go conformance' || 'conformance' }} | ${{ inputs.opentofu-only && 'conformance' || 'opentofu conformance' }}"},
		{"the base image job may assume the verify role that pulls the operator image", field("jobs.base-image.permissions.id-token"), "write"},
		{"the base image job assumes the verify role the caller names", field(stepPath(all, "base-image", "Configure AWS credentials for the verify role") + ".with.role-to-assume"), "${{ vars.AWS_BLOCK_VERIFY_ROLE_ARN }}"},
		{"the OpenTofu conformance job, which runs the block's code, holds no identity token", field("jobs.opentofu-conformance.permissions.id-token"), "<absent>"},
		{"publish grants the id-token the record's OIDC token is minted with", field("jobs.publish.permissions.id-token"), "write"},
		{"conformance runs go vet", of(conformance, "go vet", "run"), "go vet ./..."},
		{"conformance runs go test", of(conformance, "go test", "run"), "go test ./..."},
		{"the base manifest is fetched only on a pull request", ifOf(of(conformance, "Write the base commit's manifest and the changed files", "if")), "github.event_name == 'pull_request'"},
		{"the base manifest is fetched at the pull request's base sha", of(conformance, "Write the base commit's manifest and the changed files", "env.BASE_SHA"), "${{ github.event.pull_request.base.sha }}"},
		{"the report is uploaded as an artifact", strings.SplitN(of(conformance, "Upload blockverify report", "uses"), "@", 2)[0], "actions/upload-artifact"},
		{"the report is uploaded whatever the verdict, since a failing run's report must not be lost", ifOf(of(conformance, "Upload blockverify report", "if")), "always()"},
		{"the version probe runs before the image is pushed", strconv.FormatBool(indexOfID(publish, "existing") >= 0 && indexOfID(publish, "existing") < indexOf(publish, "Scan and push the image")), "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("publish-block.yml field = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// A check below matches a command inside a parsed run body, never the YAML around it.
func TestWorkflow_EachJobRunsItsCommands(t *testing.T) {
	cases := []struct {
		name, job, label string
		commands         []string
	}{
		{"conformance runs blockverify through run-blockverify.sh with its JSON report, the event and the base manifest's directory", "conformance", "blockverify (standalone contract check)", []string{`run-blockverify.sh" ./blockverify blockverify-report.json`, `"$EVENT_NAME" "$RUNNER_TEMP"`}},
		{"conformance fetches the base commit through the kit's write-pr-base.sh", "conformance", "Write the base commit's manifest and the changed files", []string{`write-pr-base.sh" "$BASE_SHA" "$RUNNER_TEMP"`}},
		{"OpenTofu conformance runs blockverify on the tree with --opentofu-only", "opentofu-conformance", "blockverify (standalone contract check)", []string{`run-blockverify.sh" "$RUNNER_TEMP/blockverify"`, "--opentofu-only"}},
		{"OpenTofu conformance checks the module's format inside the block's image", "opentofu-conformance", "tofu fmt -check", []string{`--entrypoint tofu "$IMAGE_ID" fmt -check`}},
		{"OpenTofu conformance runs the module's tests inside the block's image", "opentofu-conformance", "tofu test", []string{"tofu -chdir=/module test"}},
		{"OpenTofu conformance runs the operator's block-verify inside the block's image", "opentofu-conformance", "block-verify", []string{`--entrypoint block-verify "$IMAGE_ID" -test.v`}},
		{"OpenTofu conformance keeps block-verify's output, which the record signs", "opentofu-conformance", "block-verify", []string{"set -euo pipefail", `-test.v | tee "$RUNNER_TEMP/block-verify.txt"`}},
		{"the base image job pulls through the kit's script", "base-image", "Pull the images the block's Dockerfile names in that registry", []string{`bash block-kit/scripts/base-images.sh pull "$REGISTRY" "$RUNNER_TEMP/base-images"`}},
		{"publish pushes the image", "publish", "Scan and push the image", []string{"docker push"}},
		{"publish builds the record with the kit's tool, kept outside the directory the block's files land in", "publish", "blockprov build", []string{`"$RUNNER_TEMP/blockprov" build`}},
		{"publish pushes the record with the same tool", "publish", "blockprov push", []string{`"$RUNNER_TEMP/blockprov" push`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := script(t, tc.job, tc.label).Run

			for _, c := range tc.commands {
				if !strings.Contains(run, c) {
					t.Errorf("%s %q run =\n%s\nwant it to run %q", tc.job, tc.label, run, c)
				}
			}
		})
	}
}

func TestWorkflow_ConformanceNeverPushesOrWritesARecord(t *testing.T) {
	all := allFields(t)

	for path, v := range all {
		if !strings.HasPrefix(path, "jobs.conformance.") && !strings.HasPrefix(path, "jobs.opentofu-conformance.") {
			continue
		}
		for _, forbidden := range []string{"docker push", "blockprov push", "blockprov build"} {
			if strings.Contains(v, forbidden) {
				t.Errorf("%s = %q runs %q; a red run must produce no artefact beyond its report", path, v, forbidden)
			}
		}
	}
}

func TestWorkflow_EveryUsesIsPinnedToACommitSha(t *testing.T) {
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	n := 0

	for path, ref := range allFields(t) {
		if !strings.HasSuffix(path, ".uses") {
			continue
		}
		n++
		if _, pin, ok := strings.Cut(ref, "@"); !ok || !sha.MatchString(pin) {
			t.Errorf("%s = %q, want an action pinned to a 40-character commit sha, not a tag", path, ref)
		}
	}

	if n == 0 {
		t.Fatal("the workflow has no uses: at all, so nothing was checked")
	}
}

func TestWorkflow_HeaderStatesThePublishEventAndWhatARerunCompletes(t *testing.T) {
	var header []string
	for _, line := range strings.Split(string(readWorkflow(t)), "\n") {
		if strings.HasPrefix(line, "name:") {
			break
		}
		header = append(header, line)
	}

	text := strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(strings.Join(header, " ")), "#", " ")), " ")

	for _, want := range []string{"push", "default branch", "rerun", "provenance record"} {
		if !strings.Contains(text, want) {
			t.Errorf("the header comment must state the publish event and what a rerun completes; it lacks %q", want)
		}
	}
}

// imageSteps push the image; recordSteps build and push the provenance record; writeSteps write to the registry or mint a signed record; releaseSteps guard, cut and announce the release tag.
var (
	imageSteps  = []string{"Scan and push the image"}
	recordSteps = []string{
		"Determine the kit version pinned in go.mod",
		"blockprov build",
		"Fetch ECR password",
		"blockprov push",
	}
	opentofuRecordSteps = []string{"Determine the kit version the operator image was built with"}
	writeSteps          = []string{"Scan and push the image", "blockprov build", "Fetch ECR password", "blockprov push"}
	releaseSteps        = []string{"Refuse a version tagged at another commit", "Tag the release at the commit it built", "Start the block's publisher at the release tag"}
)

func TestWorkflow_WriteStepsGateOnTheProbe(t *testing.T) {
	steps := publishSteps(t)
	mode := map[string]string{recordSteps[0]: " && !inputs.opentofu-only", opentofuRecordSteps[0]: " && inputs.opentofu-only"}
	for _, tc := range []struct {
		names  []string
		wantIf string
	}{
		{imageSteps, "steps.existing.outputs.published == 'false'"},
		{append(slices.Clone(recordSteps), opentofuRecordSteps...), "steps.existing.outputs.record == 'false'"},
	} {
		for _, name := range tc.names {
			i := indexOf(steps, name)

			if i < 0 || ifOf(steps[i]["if"]) != tc.wantIf+mode[name] {
				t.Errorf("%q (index %d) is not gated on %q", name, i, tc.wantIf+mode[name])
			}
		}
	}
	// A skipped probe answers '', so a != gate against it would run its step.
	for _, s := range steps {
		if expr := ifOf(s["if"]); strings.Contains(expr, "steps.existing.outputs") && strings.Contains(expr, "!=") {
			t.Errorf("step %q is gated on %q; gate with == 'false' instead", s.label(), expr)
		}
	}
}

type probeResult struct {
	scriptResult
	outputs, env map[string]string
	argv         []string
}

func (r probeResult) probedTags() []string {
	var tags []string
	for _, line := range r.argv {
		for _, f := range strings.Fields(line) {
			if tag, ok := strings.CutPrefix(f, "imageTag="); ok {
				tags = append(tags, tag)
			}
		}
	}
	return tags
}

// runProbe runs the version probe against a stub aws that answers per tag: imageMode for the version tag, recordMode for the record tag and shaMode for the commit-sha tag, each "exists", "absent" or "fail", and shaMode also "match" (the image's digest) or "other".
func runProbe(t *testing.T, version, imageMode, recordMode, shaMode string) probeResult {
	t.Helper()
	steps := publishSteps(t)
	i := indexOfID(steps, "existing")
	if i < 0 {
		t.Fatal("publish job has no step with id: existing, the tag-exists probe")
	}
	tmp := t.TempDir()
	body := strings.ReplaceAll(script(t, "publish", steps[i]["name"]).Run, "${{ inputs.ecr-repository }}", "mitstack-postgres-db")
	writeExecutable(t, filepath.Join(tmp, "probe.sh"), body)
	work, stub := filepath.Join(tmp, "work"), filepath.Join(tmp, "stub")
	for _, d := range []string{work, stub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(work, "manifest.yaml"), "spec:\n  version: "+version+"\n")
	writeExecutable(t, filepath.Join(stub, "aws"), "#!/bin/sh\n"+
		"echo \"$@\" >> \"$AWS_ARGV_FILE\"\n"+
		"mode=\"\"\n"+
		"for a in \"$@\"; do\n"+
		"  case \"$a\" in\n"+
		"    imageTag=v*) mode=\"$AWS_IMAGE_MODE\" ;;\n"+
		"    imageTag=sha256-*.prov) mode=\"$AWS_RECORD_MODE\"; digest=\"$AWS_RECORD_DIGEST\" ;;\n"+
		"    imageTag=$GITHUB_SHA) mode=\"$AWS_SHA_MODE\" ;;\n"+
		"  esac\n"+
		"done\n"+
		"if [ \"$mode\" = other ]; then mode=exists; digest=\"$AWS_RECORD_DIGEST\"; fi\n"+
		"if [ \"$mode\" = match ]; then mode=exists; fi\n"+
		"[ -n \"$digest\" ] || digest=\"$AWS_DIGEST\"\n"+
		"case \"$mode\" in\n"+
		"  exists) echo \"$digest\"; exit 0 ;;\n"+
		"  absent) echo \"None\"; exit 0 ;;\n"+
		"  fail) echo \"boom: access denied\" >&2; exit 254 ;;\n"+
		"  *) echo \"stub aws: no imageTag it recognises in: $*\" >&2; exit 99 ;;\n"+
		"esac\n")
	output, env, argv := filepath.Join(tmp, "github-output"), filepath.Join(tmp, "github-env"), filepath.Join(tmp, "aws-argv")
	for _, f := range []string{output, env, argv} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res := runBash(t, filepath.Join(tmp, "probe.sh"), work,
		"PATH="+stub+":"+os.Getenv("PATH"), "GITHUB_OUTPUT="+output, "GITHUB_ENV="+env, "ECR_REPOSITORY=mitstack-postgres-db",
		"AWS_IMAGE_MODE="+imageMode, "AWS_RECORD_MODE="+recordMode, "AWS_SHA_MODE="+shaMode, "GITHUB_SHA="+testSha,
		"AWS_DIGEST="+testDigest, "AWS_RECORD_DIGEST="+testRecordDigest, "AWS_ARGV_FILE="+argv)

	read := func(f string) string { b, _ := os.ReadFile(f); return string(b) }
	return probeResult{res, keyValues(read(output)), keyValues(read(env)), strings.Split(strings.TrimSpace(read(argv)), "\n")}
}

func TestWorkflow_TheProbe(t *testing.T) {
	cases := []struct {
		name                        string
		version, image, record, sha string
		wantExit                    bool
		wantOutputs                 map[string]string
		wantImageDigestEnv          string
		wantTags                    []string
		wantStdout, wantInFailure   string
	}{
		{"a published tag with no record, built from this commit, is completed for its digest", "1.2.0", "exists", "absent", "match", true,
			map[string]string{"published": "true", "digest": testDigest, "record": "false", "built": "true", "tag": "v1.2.0"}, testDigest, []string{"v1.2.0", testRecordTag, testSha},
			"already published: v1.2.0 " + testDigest + "\nno provenance record at " + testRecordTag + "; completing it for " + testDigest + "\n", ""},
		{"a rerun of the commit that built a tag with its record writes no record and says this commit built it", "1.2.0", "exists", "exists", "match", true,
			map[string]string{"published": "true", "digest": testDigest, "record": "true", "built": "true", "tag": "v1.2.0"}, testDigest, []string{"v1.2.0", testRecordTag, testSha},
			"already published: v1.2.0 " + testDigest + "\nprovenance record present at " + testRecordTag + "\n", ""},
		{"a later commit finding a tag and its record writes nothing and says another commit built it", "1.2.0", "exists", "exists", "other", true,
			map[string]string{"published": "true", "digest": testDigest, "record": "true", "built": "false", "tag": "v1.2.0"}, testDigest, []string{"v1.2.0", testRecordTag, testSha},
			"already published: v1.2.0 " + testDigest + "\nprovenance record present at " + testRecordTag + "\n", ""},
		{"a tag with its record whose commit check cannot answer fails closed, naming the commit", "1.2.0", "exists", "exists", "fail", false,
			map[string]string{"published": "true", "digest": testDigest, "tag": "v1.2.0"}, testDigest, []string{"v1.2.0", testRecordTag, testSha}, "", testSha},
		{"an unpublished tag publishes and is never claimed as already published", "1.2.0", "absent", "absent", "match", true,
			map[string]string{"published": "false", "record": "false", "built": "true", "tag": "v1.2.0"}, "", []string{"v1.2.0"}, "", ""},
		{"an unpublished tag's record is never probed", "1.2.0", "absent", "fail", "match", true,
			map[string]string{"published": "false", "record": "false", "built": "true", "tag": "v1.2.0"}, "", []string{"v1.2.0"}, "", ""},
		{"a version bump probes and publishes its own tag", "1.3.0", "absent", "absent", "match", true,
			map[string]string{"published": "false", "record": "false", "built": "true", "tag": "v1.3.0"}, "", []string{"v1.3.0"}, "", ""},
		{"a version probe that cannot answer fails closed, naming the tag", "1.2.0", "fail", "absent", "match", false,
			map[string]string{}, "", []string{"v1.2.0"}, "", "v1.2.0"},
		{"a record probe that cannot answer fails closed, naming the record tag", "1.2.0", "exists", "fail", "match", false,
			map[string]string{"published": "true", "digest": testDigest, "tag": "v1.2.0"}, testDigest, []string{"v1.2.0", testRecordTag}, "", testRecordTag},
		{"a published image another commit built is never given this commit's record", "1.2.0", "exists", "absent", "other", false,
			map[string]string{"published": "true", "digest": testDigest, "built": "false", "tag": "v1.2.0"}, testDigest, []string{"v1.2.0", testRecordTag, testSha}, "", testSha},
		{"a published image with no sha tag is never given this commit's record", "1.2.0", "exists", "absent", "absent", false,
			map[string]string{"published": "true", "digest": testDigest, "built": "false", "tag": "v1.2.0"}, testDigest, []string{"v1.2.0", testRecordTag, testSha}, "", testSha},
		{"a commit check that cannot answer gives no record answer", "1.2.0", "exists", "absent", "fail", false,
			map[string]string{"published": "true", "digest": testDigest, "tag": "v1.2.0"}, testDigest, []string{"v1.2.0", testRecordTag, testSha}, "", testSha},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runProbe(t, tc.version, tc.image, tc.record, tc.sha)

			if (res.exitCode == 0) != tc.wantExit {
				t.Fatalf("exit code = %d, want success %v; output:\n%s", res.exitCode, tc.wantExit, res.combined)
			}
			if !maps.Equal(res.outputs, tc.wantOutputs) {
				t.Errorf("$GITHUB_OUTPUT = %v, want %v", res.outputs, tc.wantOutputs)
			}
			if got := res.env["IMAGE_DIGEST"]; got != tc.wantImageDigestEnv {
				t.Errorf("$GITHUB_ENV IMAGE_DIGEST = %q, want %q", got, tc.wantImageDigestEnv)
			}
			if got := res.probedTags(); !slices.Equal(got, tc.wantTags) {
				t.Errorf("probed tags %q, want %q", got, tc.wantTags)
			}
			if tc.wantExit && res.stdout != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", res.stdout, tc.wantStdout)
			}
			if !tc.wantExit && !strings.Contains(res.combined, tc.wantInFailure) {
				t.Errorf("output = %q, want it to name %s, the answer it could not confirm", res.combined, tc.wantInFailure)
			}
		})
	}
}

func TestWorkflow_TheProbeAsksAWSForTheRepositorysTag(t *testing.T) {
	res := runProbe(t, "1.2.0", "exists", "absent", "match")

	if want := "ecr batch-get-image --repository-name mitstack-postgres-db --image-ids imageTag=v1.2.0 --query images[0].imageId.imageDigest --output text"; res.argv[0] != want {
		t.Errorf("first aws argv = %q, want %q", res.argv[0], want)
	}
}

// stepRuns evaluates an if: for a Go block, and fails the test on any form it does not know, so a new gate cannot pass unexamined.
func stepRuns(t *testing.T, s step, outputs map[string]string) bool {
	t.Helper()
	switch expr := ifOf(s["if"]); expr {
	case "", "always()":
		return true
	default:
		m := regexp.MustCompile(`^steps\.existing\.outputs\.(\w+) == '([^']*)'( && !?inputs\.opentofu-only)?$`).FindStringSubmatch(expr)
		if m == nil {
			t.Fatalf("step if: %q is not a form this evaluator knows", expr)
		}
		return outputs[m[1]] == m[2] && m[3] != " && inputs.opentofu-only"
	}
}

func TestWorkflow_TheProbeAnswerDecidesWhichWriteStepsRun(t *testing.T) {
	cases := []struct {
		name          string
		probe         []string
		wantRun, skip []string
	}{
		{"a rerun completes the record for a published tag without pushing the image, and cuts its release tag", []string{"1.2.0", "exists", "absent", "match"}, slices.Concat(recordSteps, releaseSteps), imageSteps},
		{"a rerun of the commit that built a tag with its record writes no image or record and cuts its release tag", []string{"1.2.0", "exists", "exists", "match"}, releaseSteps, writeSteps},
		{"a later commit finding a tag and its record writes nothing and cuts no release tag", []string{"1.2.0", "exists", "exists", "other"}, nil, slices.Concat(writeSteps, releaseSteps)},
		{"an unpublished version runs the image, record and release steps", []string{"1.3.0", "absent", "absent", "match"}, slices.Concat(imageSteps, recordSteps, releaseSteps), nil},
		{"a probe that was itself skipped answers nothing, so no write or release step runs", nil, nil, slices.Concat(writeSteps, releaseSteps)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputs := map[string]string{}
			if tc.probe != nil {
				res := runProbe(t, tc.probe[0], tc.probe[1], tc.probe[2], tc.probe[3])
				if res.exitCode != 0 {
					t.Fatalf("probe exit code = %d; output:\n%s", res.exitCode, res.combined)
				}
				outputs = res.outputs
			}
			steps := publishSteps(t)

			for _, want := range []struct {
				names []string
				runs  bool
			}{{tc.wantRun, true}, {tc.skip, false}} {
				for _, name := range want.names {
					i := indexOf(steps, name)
					if i < 0 {
						t.Fatalf("publish job has no step named %q", name)
					}
					if got := stepRuns(t, steps[i], outputs); got != want.runs {
						t.Errorf("step %q runs = %v under %v, want %v", name, got, outputs, want.runs)
					}
				}
			}
		})
	}
}

func runStepBody(t *testing.T, name string, env map[string]string) string {
	t.Helper()
	literals := map[string]string{
		"inputs.block-id":       "postgres-db",
		"inputs.ecr-repository": "mitstack-postgres-db",
		"inputs.aws-region":     "eu-central-1",
		"github.repository":     "mitstack-eu/postgres-db",
		"github.server_url":     "https://github.com",
		"github.run_id":         "42",
	}
	body := regexp.MustCompile(`\$\{\{\s*([^}]+?)\s*\}\}`).ReplaceAllStringFunc(script(t, "publish", name).Run, func(s string) string {
		key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "${{"), "}}"))
		v, ok := literals[key]
		if !ok {
			t.Fatalf("step %q uses ${{ %s }}, which this test has no literal for", name, key)
		}
		return v
	})
	tmp := t.TempDir()
	argvFile := filepath.Join(tmp, "blockprov-argv")
	writeExecutable(t, filepath.Join(tmp, "blockprov"), "#!/bin/sh\necho \"$@\" >> \""+argvFile+"\"\n")
	writeExecutable(t, filepath.Join(tmp, "manifest.yaml"), "spec:\n  version: 1.2.0\n")
	writeExecutable(t, filepath.Join(tmp, "step.sh"), body)
	vars := []string{"RUNNER_TEMP=" + tmp}
	for k, v := range env {
		vars = append(vars, k+"="+v)
	}

	if res := runBash(t, filepath.Join(tmp, "step.sh"), tmp, vars...); res.exitCode != 0 {
		t.Fatalf("step %q failed: exit %d\n%s", name, res.exitCode, res.combined)
	}

	argv, _ := os.ReadFile(argvFile)
	return string(argv)
}

func TestWorkflow_ARerunBuildsAndPushesTheRecordForThePublishedDigest(t *testing.T) {
	res := runProbe(t, "1.2.0", "exists", "absent", "match")
	if res.exitCode != 0 {
		t.Fatalf("probe exit code = %d; output:\n%s", res.exitCode, res.combined)
	}
	env := res.env
	for k, v := range map[string]string{
		"REGISTRY": "123456789012.dkr.ecr.eu-central-1.amazonaws.com", "KIT_VERSION": "v0.9.0", "ECR_PASSWORD": "stub-password",
		"GITHUB_SHA": strings.Repeat("f", 40), "ACTIONS_ID_TOKEN_REQUEST_URL": "https://token.example", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "stub-token",
	} {
		env[k] = v
	}

	build, push := runStepBody(t, "blockprov build", env), runStepBody(t, "blockprov push", env)
	env["OPENTOFU_ONLY"] = "true"
	opentofu := runStepBody(t, "blockprov build", env)

	for _, want := range []struct{ argv, flag string }{
		{build, "--report blockverify-report.json"},
		{opentofu, "--block-verify block-verify.txt"},
		{build, "--image-digest " + testDigest},
		{build, "--image-ref " + env["REGISTRY"] + "/mitstack-postgres-db@" + testDigest},
		{push, "--image-digest " + testDigest},
	} {
		if !strings.Contains(want.argv, want.flag) {
			t.Errorf("blockprov argv = %q, want %q", want.argv, want.flag)
		}
	}
}

// Trivy reads these files from its working directory with no flag, so one anywhere in the tree fails whichever conformance job holds the tree, straight after the checkout.
func TestWorkflow_ATrivyIgnoreOrConfigFileFailsTheConformanceJob(t *testing.T) {
	const name = "Refuse a committed Trivy ignore or config file"
	all := allFields(t)
	for _, job := range []string{"conformance", "opentofu-conformance"} {
		steps := jobSteps(t, all, job)
		if i := indexOf(steps, name); i != 1 || steps[i]["if"] != "" {
			t.Fatalf("the refusal is %s step %d, want an unconditional step 1", job, i)
		}
		refuse := filepath.Join(t.TempDir(), "refuse.sh")
		writeExecutable(t, refuse, script(t, job, name).Run)

		for _, file := range []string{"", ".trivyignore", ".trivyignore.yaml", "trivy.yaml", "deploy/trivy-secret.yaml"} {
			tree := t.TempDir()
			_ = os.MkdirAll(filepath.Join(tree, "deploy"), 0o755)
			_ = os.WriteFile(filepath.Join(tree, file), nil, 0o644)

			res := runBash(t, refuse, tree)

			if (res.exitCode != 0) != (file != "") || !strings.Contains(res.stderr, file) {
				t.Errorf("%s with %q in the tree: exit %d, stderr %q", job, file, res.exitCode, res.stderr)
			}
		}
	}
}

func TestWorkflow_AFindingStopsThePushOfTheImageItScanned(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "block-kit", "scripts"), 0o755)
	writeExecutable(t, filepath.Join(dir, "docker"), "#!/bin/sh\necho docker $*\n")
	writeExecutable(t, filepath.Join(dir, "block-kit", "scripts", "scan-image.sh"), "echo scan-image.sh \"$@\"\nexit 1\n")
	writeExecutable(t, filepath.Join(dir, "manifest.yaml"), "spec:\n  version: 1.2.0\n")
	writeExecutable(t, filepath.Join(dir, "build.sh"), strings.ReplaceAll(script(t, "publish", "Scan and push the image").Run, "${{ inputs.ecr-repository }}", "repo"))

	res := runBash(t, filepath.Join(dir, "build.sh"), dir, "PATH="+dir+":"+os.Getenv("PATH"), "RUNNER_TEMP="+dir, "REGISTRY=r", "GITHUB_SHA=c0ffee")

	want := "docker load --input image.tar\ndocker tag block:c0ffee r/repo:v1.2.0\ndocker tag block:c0ffee r/repo:c0ffee\nscan-image.sh r/repo:c0ffee\n"
	if res.exitCode == 0 || res.stdout != want {
		t.Errorf("exit %d, output =\n%s\nwant the load of the image the conformance job built, then the kit's scan of its sha tag failing the step before any push:\n%s", res.exitCode, res.combined, want)
	}
}

// The kit is a public module, so its checkout passes no token of its own and no image build takes a secret.
func TestWorkflow_JobsFetchThePublicKitWithNoCredential(t *testing.T) {
	all := allFields(t)
	for _, tc := range []struct {
		job     string
		idToken string
		want    []string
	}{
		{"conformance", "<absent>", []string{
			"actions/checkout", "Refuse a committed Trivy ignore or config file", "actions/setup-go", "go vet", "Log in to ghcr.io with the job's token", "go test", "Checkout block-kit", "Build blockverify", "Build commentcheck", "Build checkpolls",
			"Remove the kit checkout from the block tree", "Write the base commit's manifest and the changed files", "Fail a pull request that adds a fixed sleep or a polling interval", "Judge the comments this pull request adds",
			"blockverify (standalone contract check)", "Upload blockverify report", "Build the block image", "Scan the block image", "Collect what the release signs", "Hand the release to the publish job",
		}},
		{"base-image", "write", []string{
			"Checkout block-kit", "Configure AWS credentials for the verify role", "Log in to the operator image's registry", "Pull the images the block's Dockerfile names in that registry", "Hand the base images to the conformance job",
		}},
		{"opentofu-conformance", "<absent>", []string{
			"actions/checkout", "Refuse a committed Trivy ignore or config file", "actions/setup-go", "Checkout block-kit", "Build blockverify", "Build checkpolls", "Remove the kit checkout from the block tree",
			"Write the base commit's manifest and the changed files", "blockverify (standalone contract check)", "Upload blockverify report", "Fail a pull request that adds a fixed sleep or a polling interval", "Stop where the base image job did not succeed", "Fetch the base images", "Log in to ghcr.io with the job's token", "Build the block image", "tofu fmt -check", "tofu test", "block-verify", "Scan the block image", "Collect what the release signs", "Hand the release to the publish job",
		}},
		{"secrets", "<absent>", []string{"actions/checkout", "Checkout block-kit", "Scan every commit for secrets with gitleaks", "Fail a shipped file that fetches a tool from a GitHub release page"}},
		{"scripts", "<absent>", []string{"actions/checkout", "Checkout block-kit", "Lint every tracked shell script with shellcheck"}},
		{"publish", "write", []string{
			"actions/setup-go", "Configure AWS credentials", "Checkout block-kit", "Build blockprov", "Move the kit out of the working directory", "Log in to ECR",
			"Fetch what the release signs", "Probe the version tag and its provenance record", "Refuse a version tagged at another commit", "Scan and push the image", completesOnlyTheHandedImage,
			"Determine the kit version pinned in go.mod", "Determine the kit version the operator image was built with",
			"blockprov build", "Fetch ECR password", "blockprov push", "Tag the release at the commit it built", "Start the block's publisher at the release tag",
		}},
	} {
		t.Run(tc.job, func(t *testing.T) {
			steps := jobSteps(t, all, tc.job)
			idToken, ok := all["jobs."+tc.job+".permissions.id-token"]
			if !ok {
				idToken = "<absent>"
			}

			checkout := indexOf(steps, "Checkout block-kit")

			if got := labels(steps); !slices.Equal(got, tc.want) {
				t.Errorf("%s steps =\n  %s\nwant\n  %s", tc.job, strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
			if checkout < 0 {
				t.Fatalf("%s job has no step %q", tc.job, "Checkout block-kit")
			}
			if token, ok := steps[checkout]["with.token"]; ok {
				t.Errorf("%s kit checkout passes token %q, want none, so the public kit is read with the run's default token", tc.job, token)
			}
			if idToken != tc.idToken {
				t.Errorf("%s id-token permission = %q, want %q, since among these jobs only publish and base-image assume an AWS role, and only publish mints the record's OIDC token", tc.job, idToken, tc.idToken)
			}
			if i := indexOf(steps, "Build the block image"); i >= 0 && strings.Contains(steps[i]["run"], "--secret") {
				t.Errorf("%s image build run =\n%s\nwant no --secret, so the image fetches the public kit with no credential", tc.job, steps[i]["run"])
			}
		})
	}
}

// The kit checkout must be gone before blockverify or the image build reads the tree, or the block is judged by the kit's own source. In publish it must be gone before the block's files arrive, or one of them could replace a kit script.
func TestWorkflow_TheKitCheckoutLeavesTheBlockTreeBeforeItIsJudged(t *testing.T) {
	all := allFields(t)
	for _, tc := range []struct{ job, name, build, firstReader string }{
		{"conformance", "Remove the kit checkout from the block tree", "Build blockverify", "blockverify (standalone contract check)"},
		{"opentofu-conformance", "Remove the kit checkout from the block tree", "Checkout block-kit", "Build the block image"},
		{"publish", "Move the kit out of the working directory", "Build blockprov", "Fetch what the release signs"},
	} {
		t.Run(tc.job, func(t *testing.T) {
			name := tc.name
			steps := jobSteps(t, all, tc.job)
			rm, b, r := indexOf(steps, name), indexOf(steps, tc.build), indexOf(steps, tc.firstReader)
			if rm < 0 {
				t.Fatalf("%s job has no step %q", tc.job, name)
			}
			tree := t.TempDir()
			if err := os.MkdirAll(filepath.Join(tree, "block-kit", "events"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(tree, "block-kit", "events", "events.go"), "package events\n")
			writeExecutable(t, filepath.Join(tree, "main.go"), "package main\n")
			rmScript := filepath.Join(t.TempDir(), "rm.sh")
			writeExecutable(t, rmScript, script(t, tc.job, name).Run)

			temp := t.TempDir()
			res := runBash(t, rmScript, tree, "RUNNER_TEMP="+temp)

			if _, err := os.Stat(filepath.Join(temp, "block-kit", "events", "events.go")); err != nil {
				t.Errorf("the kit under RUNNER_TEMP after the step: stat err %v, want it there, since the image scan runs its scripts", err)
			}
			if b < 0 || rm < b || r < 0 || rm > r {
				t.Errorf("%q is at %d, want it after %q at %d and before %q at %d", name, rm, tc.build, b, tc.firstReader, r)
			}
			if got := ifOf(steps[rm]["if"]); got != "" {
				t.Errorf("%q has if: %q, want none", name, got)
			}
			if res.exitCode != 0 {
				t.Fatalf("step failed: exit %d\n%s", res.exitCode, res.combined)
			}
			if _, err := os.Stat(filepath.Join(tree, "block-kit")); !os.IsNotExist(err) {
				t.Errorf("block-kit/ still exists after the step (stat err %v)", err)
			}
			if _, err := os.Stat(filepath.Join(tree, "main.go")); err != nil {
				t.Errorf("the block's own main.go is gone: %v", err)
			}
		})
	}
}

func TestWorkflow_KitVersionStepReadsTheKitPinFromGoMod(t *testing.T) {
	const name = "Determine the kit version pinned in go.mod"
	for _, tc := range []struct {
		name, goMod, want string
	}{
		{"a block requires block-kit's module path",
			"module github.com/mitstack-eu/postgres-db\n\ngo 1.26\n\nrequire github.com/mitstack-eu/block-kit v0.4.0\n", "v0.4.0"},
		{"a block lists block-kit inside a require block",
			"module github.com/mitstack-eu/kube-app-deploy\n\ngo 1.26\n\nrequire (\n\tgithub.com/google/uuid v1.6.0\n\tgithub.com/mitstack-eu/block-kit v0.4.1\n)\n", "v0.4.1"},
		{"a go.mod requiring operator-kit instead of block-kit fails",
			"module github.com/mitstack-eu/kube-app-deploy\n\ngo 1.26\n\nrequire github.com/mitstack-eu/operator-kit/v3 v3.2.1\n", ""},
		{"a go.mod requiring only a sibling module whose path starts with block-kit fails",
			"module github.com/mitstack-eu/stray\n\ngo 1.26\n\nrequire github.com/mitstack-eu/block-kit-extras v1.0.0\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := script(t, "publish", name).Run
			if strings.TrimSpace(body) == "" || strings.Contains(body, "${{") {
				t.Fatalf("step %q has no plain run: script to execute:\n%s", name, body)
			}
			tmp := t.TempDir()
			block, envFile := filepath.Join(tmp, "block"), filepath.Join(tmp, "github-env")
			if err := os.MkdirAll(block, 0o755); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(tmp, "step.sh"), body)
			writeExecutable(t, envFile, "")
			writeExecutable(t, filepath.Join(block, "go.mod"), tc.goMod)

			res := runBash(t, filepath.Join(tmp, "step.sh"), block, "GITHUB_ENV="+envFile)

			envBytes, _ := os.ReadFile(envFile)
			got, set := keyValues(string(envBytes))["KIT_VERSION"]
			switch {
			case tc.want != "" && (res.exitCode != 0 || got != tc.want):
				t.Errorf("exit %d, KIT_VERSION = %q; want 0 and %q; output:\n%s", res.exitCode, got, tc.want, res.combined)
			case tc.want == "" && (res.exitCode != 1 || set || !strings.Contains(res.stderr, "no block-kit version pin found in go.mod")):
				t.Errorf("exit %d, KIT_VERSION set %v, stderr %q; want exit 1, no KIT_VERSION and the step's failure message", res.exitCode, set, res.stderr)
			}
		})
	}
}

func TestWorkflow_AnOpenTofuBlocksKitVersionIsTheOneItsOperatorImageWasBuiltWith(t *testing.T) {
	tmp := t.TempDir()
	writeExecutable(t, filepath.Join(tmp, "docker"), "#!/bin/sh\necho docker \"$@\" >&2\n[ \"$1\" = create ] && echo c0ntainer\nexit 0\n")
	writeExecutable(t, filepath.Join(tmp, "go"), "#!/bin/sh\n[ \"$GOTOOLCHAIN\" = local ] || { echo 'go: a go.mod in this directory may choose the toolchain' >&2; exit 1; }\nprintf '%s: go1.27.3\\n\\tdep\\tgithub.com/mitstack-eu/block-kit-extras\\tv9.9.9\\n\\tdep\\tgithub.com/mitstack-eu/block-kit\\tv0.5.1\\th1:x=\\n' \"$3\"\n")
	writeExecutable(t, filepath.Join(tmp, "kit-version.sh"), strings.ReplaceAll(script(t, "publish", opentofuRecordSteps[0]).Run, "${{ inputs.ecr-repository }}", "repo"))
	envFile := filepath.Join(tmp, "github-env")

	res := runBash(t, filepath.Join(tmp, "kit-version.sh"), tmp, "PATH="+tmp+":"+os.Getenv("PATH"), "RUNNER_TEMP="+tmp, "REGISTRY=r", "IMAGE_DIGEST="+testDigest, "GITHUB_ENV="+envFile)

	env, _ := os.ReadFile(envFile)
	if got := keyValues(string(env))["KIT_VERSION"]; res.exitCode != 0 || got != "v0.5.1" || !strings.Contains(res.stderr, "docker create r/repo@"+testDigest) {
		t.Errorf("exit %d, KIT_VERSION %q; want 0 and v0.5.1 read from the operator binary in r/repo@%s by the installed Go alone; output:\n%s", res.exitCode, got, testDigest, res.combined)
	}
}

func stepPath(all map[string]string, job, label string) string {
	for i := 0; ; i++ {
		prefix := "jobs." + job + ".steps." + strconv.Itoa(i)
		if _, ok := all[prefix]; !ok {
			return "jobs." + job + ".steps.<no step " + label + ">"
		}
		action, _, _ := strings.Cut(all[prefix+".uses"], "@")
		if all[prefix+".name"] == label || (all[prefix+".name"] == "" && action == label) {
			return prefix
		}
	}
}

func jobNames(all map[string]string) []string {
	var jobs []string
	for path := range all {
		if job, ok := strings.CutPrefix(path, "jobs."); ok && !strings.Contains(job, ".") {
			jobs = append(jobs, job)
		}
	}
	slices.Sort(jobs)
	return jobs
}

var runsAnImage = regexp.MustCompile(`\bdocker\s+(buildx\s+build|build|run|start|exec|compose)\b`)

// blockCodeIn names the first of steps that runs the block's own code, by checking out the caller's tree or by building or running an image, and answers "" when none does.
func blockCodeIn(steps []step) string {
	for _, s := range steps {
		action, _, _ := strings.Cut(s["uses"], "@")
		switch {
		case action == "actions/checkout" && s["with.repository"] == "":
			return "its step " + strconv.Quote(s.label()) + " checks out the block's tree"
		case runsAnImage.MatchString(s["run"]):
			return "its step " + strconv.Quote(s.label()) + " builds or runs an image"
		}
	}
	return ""
}

// tokenFindings names each way workflow lets the block's own code ask GitHub for an identity token.
func tokenFindings(t *testing.T, workflow string) []string {
	t.Helper()
	all := workflowFields(workflow)
	var findings []string
	switch defaults, ok := all["permissions"]; {
	case !ok:
		findings = append(findings, "the workflow sets no permissions, so a job that sets none takes every permission its caller grants")
	case defaults != "" && defaults != "{}":
		findings = append(findings, "the workflow grants permissions: "+defaults+" to every job that sets none")
	}
	if granted, ok := all["permissions.id-token"]; ok {
		findings = append(findings, "the workflow grants id-token: "+granted+" to every job that sets no permissions")
	}
	for _, job := range jobNames(all) {
		granted, ok := all["jobs."+job+".permissions.id-token"]
		if whole := all["jobs."+job+".permissions"]; whole != "" && whole != "{}" {
			granted, ok = whole, true
		}
		if why := blockCodeIn(jobSteps(t, all, job)); ok && why != "" {
			findings = append(findings, "job "+job+" holds id-token: "+granted+", and "+why)
		}
	}
	return findings
}

// grantToken returns workflow with id-token: write added to the permissions of job, or of the workflow when job is "".
func grantToken(t *testing.T, workflow, job string) string {
	t.Helper()
	header, indent := "", ""
	if job != "" {
		header, indent = "  "+job+":", "    "
	}
	lines := strings.Split(workflow, "\n")
	inScope := job == ""
	for i, line := range lines {
		if job != "" && line == header {
			inScope = true
		}
		if inScope && line == indent+"permissions:" {
			return strings.Join(slices.Insert(lines, i+1, indent+"  id-token: write"), "\n")
		}
	}
	t.Fatalf("publish-block.yml has no permissions: mapping to add id-token to for job %q", job)
	return ""
}

func TestWorkflow_NoJobThatRunsTheBlocksCodeCanAskForAnIdentityToken(t *testing.T) {
	workflow := string(readWorkflow(t))
	all := workflowFields(workflow)
	var runBlockCode, holdAToken []string
	for _, job := range jobNames(all) {
		if blockCodeIn(jobSteps(t, all, job)) != "" {
			runBlockCode = append(runBlockCode, job)
		}
		if _, ok := all["jobs."+job+".permissions.id-token"]; ok {
			holdAToken = append(holdAToken, job)
		}
	}

	findings := tokenFindings(t, workflow)

	if len(findings) != 0 {
		t.Errorf("publish-block.yml lets the block's code ask for an identity token:\n  %s", strings.Join(findings, "\n  "))
	}
	if want := []string{"attest", "conformance", "delivery", "opentofu-conformance", "platform-verdict", "scripts", "secrets"}; !slices.Equal(runBlockCode, want) {
		t.Errorf("the jobs that check out the block's tree or run its image are %q, want %q, so the rule is read against every one of them", runBlockCode, want)
	}
	if want := []string{"base-image", "publish"}; !slices.Equal(holdAToken, want) {
		t.Errorf("the jobs holding an identity token are %q, want %q: the one that pulls the base image and the one that signs", holdAToken, want)
	}
}

func TestWorkflow_GrantingATokenToAJobThatRunsTheBlocksCodeIsNamed(t *testing.T) {
	workflow := string(readWorkflow(t))
	const checksOut = `, and its step "actions/checkout" checks out the block's tree`
	for _, tc := range []struct{ job, want string }{
		{"", "the workflow grants id-token: write to every job that sets no permissions"},
		{"conformance", "job conformance holds id-token: write" + checksOut},
		{"opentofu-conformance", "job opentofu-conformance holds id-token: write" + checksOut},
		{"secrets", "job secrets holds id-token: write" + checksOut},
		{"scripts", "job scripts holds id-token: write" + checksOut},
		{"delivery", "job delivery holds id-token: write" + checksOut},
		{"platform-verdict", "job platform-verdict holds id-token: write" + checksOut},
		{"attest", "job attest holds id-token: write" + checksOut},
	} {
		t.Run("a grant to "+cmp.Or(tc.job, "the whole workflow"), func(t *testing.T) {
			granted := grantToken(t, workflow, tc.job)

			got := tokenFindings(t, granted)

			if !slices.Contains(got, tc.want) {
				t.Errorf("findings after the grant = %q, want one reading %q", got, tc.want)
			}
		})
	}
}

// An image runs the block's code as a checkout's tests do, so a step that builds or runs one where the token is held is named too.
func TestWorkflow_BuildingOrRunningAnImageWhereTheTokenIsHeldIsNamed(t *testing.T) {
	for _, command := range []string{"docker build -t block .", `docker buildx build "${contexts[@]}" .`, `docker run --rm "$IMAGE"`, "DOCKER_BUILDKIT=1 docker  build ."} {
		t.Run(command, func(t *testing.T) {
			workflow := "permissions:\n  contents: read\njobs:\n  sign:\n    permissions:\n      id-token: write\n    steps:\n      - name: Try the image\n        run: " + command + "\n"

			got := tokenFindings(t, workflow)

			if want := []string{`job sign holds id-token: write, and its step "Try the image" builds or runs an image`}; !slices.Equal(got, want) {
				t.Errorf("findings = %q, want %q", got, want)
			}
		})
	}
}

// The only job that asks for the token that signs the record handles the block's files as data: its tools are built outside the working directory before they arrive, and no later step runs a file from where they land.
func TestWorkflow_TheJobThatSignsRunsNoneOfTheBlocksCode(t *testing.T) {
	all := allFields(t)
	var signers, writers []string
	for _, job := range jobNames(all) {
		for _, s := range jobSteps(t, all, job) {
			if strings.Contains(s["run"], "--token-request-token") && !slices.Contains(signers, job) {
				signers = append(signers, job)
			}
			for _, write := range []string{"docker push", `blockprov" push`, `release-tag.sh" push`} {
				if strings.Contains(s["run"], write) && !slices.Contains(writers, job) {
					writers = append(writers, job)
				}
			}
		}
	}
	steps := publishSteps(t)
	built, moved, fetched := indexOf(steps, "Build blockprov"), indexOf(steps, "Move the kit out of the working directory"), indexOf(steps, "Fetch what the release signs")
	if built < 0 || moved < 0 || fetched < 0 {
		t.Fatalf("publish builds blockprov at step %d, moves the kit out at %d and fetches the block's files at %d, want all three; its steps are %q", built, moved, fetched, labels(steps))
	}
	var afterTheFiles []string
	fromHere := regexp.MustCompile(`(^|[\s"'(;|&])\./`)
	for _, s := range steps[fetched+1:] {
		if s["uses"] != "" || s["working-directory"] != "" || strings.Contains(s["run"], "go build") || fromHere.MatchString(s["run"]) {
			afterTheFiles = append(afterTheFiles, s.label())
		}
	}
	cases := []struct{ name, got, want string }{
		{"one job asks GitHub for the token that signs the record", strings.Join(signers, " "), "publish"},
		{"the job that signs is the only one that pushes an image, a record or a tag", strings.Join(writers, " "), "publish"},
		{"the job that signs checks out none of the block's tree and never builds or runs its image", blockCodeIn(steps), ""},
		{"the block's files arrive under the name the conformance jobs upload", strings.SplitN(steps[fetched]["uses"], "@", 2)[0] + " " + steps[fetched]["with.name"], "actions/download-artifact block-release"},
		{"the block's files arrive in the working directory, where the probe and blockprov read them", steps[fetched]["with.path"], ""},
		{"blockprov is built into RUNNER_TEMP, where no file of the block's can replace it", steps[built]["run"], `go build -o "$RUNNER_TEMP/blockprov" ./verify/blockprov`},
		{"the kit's tools are built and moved out before the block's files arrive", strconv.FormatBool(built < moved && moved < fetched), "true"},
		{"no step after the block's files arrive runs an action, builds, or runs a file from the directory they land in", strings.Join(afterTheFiles, ", "), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("publish-block.yml: got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

const completesOnlyTheHandedImage = "Refuse to complete the record of an image this run was not handed"

// The record signs the verdict of the image the run was handed, and a rerun of every job hands over a new build, so a record is completed only where the published image is the one handed over.
func TestWorkflow_ARecordIsCompletedOnlyForTheImageThisRunWasHanded(t *testing.T) {
	steps := publishSteps(t)
	i := indexOf(steps, completesOnlyTheHandedImage)
	if i < 0 || i > indexOf(steps, "blockprov build") || ifOf(steps[i]["if"]) != "steps.existing.outputs.published == 'true' && steps.existing.outputs.record == 'false'" {
		t.Fatalf("publish steps = %q, want %q before blockprov build, run only where a published image has no record", labels(steps), completesOnlyTheHandedImage)
	}
	for _, tc := range []struct {
		name, published string
		wantExit        bool
	}{
		{"the published image is the one handed over, so its record is completed", "handed", true},
		{"another image is published, so the step fails naming it", "rebuilt", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeExecutable(t, filepath.Join(dir, "docker"), "#!/bin/sh\nfor last; do :; done\ncase \"$1 $2 $last\" in\n  'image inspect block:c0ffee') echo handed ;;\n  'image inspect '*) echo \"$PUBLISHED\" ;;\n  *) echo \"docker $*\" ;;\nesac\n")
			writeExecutable(t, filepath.Join(dir, "step.sh"), strings.ReplaceAll(script(t, "publish", completesOnlyTheHandedImage).Run, "${{ inputs.ecr-repository }}", "repo"))

			res := runBash(t, filepath.Join(dir, "step.sh"), dir, "PATH="+dir+":"+os.Getenv("PATH"), "REGISTRY=r", "GITHUB_SHA=c0ffee", "IMAGE_DIGEST="+testDigest, "PUBLISHED="+tc.published)

			if want := "docker load --input image.tar\ndocker pull r/repo@" + testDigest + "\n"; (res.exitCode == 0) != tc.wantExit || res.stdout != want {
				t.Errorf("exit %d, want success %v; stdout =\n%s\nwant the handed-over image loaded and the published one pulled:\n%s", res.exitCode, tc.wantExit, res.stdout, want)
			}
			if !tc.wantExit && (!strings.Contains(res.stderr, "r/repo@"+testDigest) || !strings.Contains(res.stderr, "Re-run only the failed jobs")) {
				t.Errorf("stderr = %q, want it to name the published image and the rerun that completes its record", res.stderr)
			}
		})
	}
}

func TestWorkflow_AFailedSuiteStartsNoSigningAndHandsNothingOver(t *testing.T) {
	all := allFields(t)
	cases := []struct{ name, got, want string }{
		{"publish waits for both conformance jobs", all["jobs.publish.needs"], "[conformance, opentofu-conformance, secrets, scripts]"},
		{"publish runs only when no job before it failed", strconv.FormatBool(strings.Contains(all["jobs.publish.if"], "!failure()")), "true"},
	}
	for _, tc := range []struct {
		job    string
		checks []string
	}{
		{"conformance", []string{"go test", "blockverify (standalone contract check)", "Build the block image"}},
		{"opentofu-conformance", []string{"blockverify (standalone contract check)", "tofu fmt -check", "tofu test", "block-verify"}},
	} {
		steps := jobSteps(t, all, tc.job)
		collected, handed := indexOf(steps, "Collect what the release signs"), indexOf(steps, "Hand the release to the publish job")
		if collected < 0 || handed < 0 {
			t.Fatalf("%s collects the release at step %d and hands it over at %d, want both; its steps are %q", tc.job, collected, handed, labels(steps))
		}
		for _, check := range tc.checks {
			cases = append(cases, struct{ name, got, want string }{tc.job + " collects the release after " + check, strconv.FormatBool(indexOf(steps, check) >= 0 && indexOf(steps, check) < collected && collected < handed), "true"})
		}
		cases = append(cases,
			struct{ name, got, want string }{tc.job + " collects only on a push that publishes, and only when every step before it passed", steps[collected]["if"], "env.PUBLISHES == 'true'"},
			struct{ name, got, want string }{tc.job + " hands over on the same condition", steps[handed]["if"], "env.PUBLISHES == 'true'"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("publish-block.yml: got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestWorkflow_APublishingPushHandsTheReleaseToThePublishJob(t *testing.T) {
	all := allFields(t)
	const collect = `bash "$RUNNER_TEMP/block-kit/scripts/collect-release.sh" "block:${GITHUB_SHA}" "$RUNNER_TEMP/block-release" "$PUBLISHER_WORKFLOW" manifest.yaml `
	cases := []struct{ name, got, want string }{
		{"a push to the default branch of a caller naming a registry is the one event that publishes", all["env.PUBLISHES"],
			"${{ inputs.ecr-repository != '' && github.event_name == 'push' && github.ref == format('refs/heads/{0}', github.event.repository.default_branch) }}"},
		{"a Go block's image is built on a pull request, which scans it, and on a push that publishes it", all[stepPath(all, "conformance", "Build the block image")+".if"], "github.event_name == 'pull_request' || env.PUBLISHES == 'true'"},
		{"a Go block hands over its go.mod, which names the kit version, and blockverify's report", all[stepPath(all, "conformance", "Collect what the release signs")+".run"], collect + "go.mod blockverify-report.json"},
		{"an OpenTofu block hands over block-verify's output", all[stepPath(all, "opentofu-conformance", "Collect what the release signs")+".run"], collect + `"$RUNNER_TEMP/block-verify.txt"`},
	}
	for _, job := range []string{"conformance", "opentofu-conformance"} {
		handed := stepPath(all, job, "Hand the release to the publish job")
		cases = append(cases,
			struct{ name, got, want string }{job + " learns the publisher workflow the caller names", all[stepPath(all, job, "Collect what the release signs")+".env.PUBLISHER_WORKFLOW"], "${{ inputs.publisher-workflow }}"},
			struct{ name, got, want string }{job + " uploads the release under the name publish fetches", strings.SplitN(all[handed+".uses"], "@", 2)[0] + " " + all[handed+".with.name"], "actions/upload-artifact block-release"},
			struct{ name, got, want string }{job + " uploads the directory the collection wrote", all[handed+".with.path"], "${{ runner.temp }}/block-release"},
			struct{ name, got, want string }{job + " replaces the release an earlier attempt of the run handed over", all[handed+".with.overwrite"], "true"},
			struct{ name, got, want string }{job + " uploads the publisher workflow too, which sits under a hidden directory", all[handed+".with.include-hidden-files"], "true"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("publish-block.yml: got %q, want %q", tc.got, tc.want)
			}
		})
	}
}
