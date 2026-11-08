package scripts_test

// publish-block.yml runs the kit's scan scripts on every event before publish, scans a pull request's image with the publish job's own scan, publishes only for a caller naming its image registry, hands blockverify in either conformance job what a pull request changes, and asks the platform for its verdict and its attestation where a starter turns them on.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func publishWorkflow(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../.github/workflows/publish-block.yml")
	if err != nil {
		t.Fatalf("reading publish-block.yml: %v", err)
	}
	return workflowFields(string(raw))
}

func TestPublishBlockWorkflow_ScansEveryEventBeforePublish(t *testing.T) {
	all := publishWorkflow(t)
	field := func(path string) string {
		if v, ok := all[path]; ok {
			return v
		}
		return "<absent>"
	}
	step := func(job, key, value string) string { return strconv.FormatBool(stepWith(all, job, key, value)) }
	cases := []struct{ name, got, want string }{
		{"the secrets job carries no if, so every event scans", field("jobs.secrets.if"), "<absent>"},
		{"the scripts job carries no if, so every event lints", field("jobs.scripts.if"), "<absent>"},
		{"the secrets job reads contents alone", field("jobs.secrets.permissions.contents"), "read"},
		{"the scripts job reads contents alone", field("jobs.scripts.permissions.contents"), "read"},
		{"the secrets job checks the block out beside the kit", step("secrets", "with.path", "block"), "true"},
		{"the secrets job checks out every commit the block's HEAD reaches", step("secrets", "with.fetch-depth", "0"), "true"},
		{"the secrets job takes its scan from kit-ref", step("secrets", "with.ref", "${{ inputs.kit-ref }}"), "true"},
		{"the secrets job scans the block with the kit's script", step("secrets", "run", "bash block-kit/scripts/scan-secrets.sh block"), "true"},
		{"the scripts job checks the block out beside the kit", step("scripts", "with.path", "block"), "true"},
		{"the scripts job takes its lint from kit-ref", step("scripts", "with.ref", "${{ inputs.kit-ref }}"), "true"},
		{"the scripts job lints the block with the kit's script", step("scripts", "run", "bash block-kit/scripts/lint-scripts.sh block"), "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("publish-block.yml field = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestPublishBlockWorkflow_APullRequestRunsThePublishScan(t *testing.T) {
	const scan, image = `bash "$RUNNER_TEMP/block-kit/scripts/scan-image.sh" `, `"block:${GITHUB_SHA}"`
	raw, err := os.ReadFile("../.github/workflows/publish-block.yml")
	if err != nil {
		t.Fatal(err)
	}
	all := workflowFields(string(raw))
	if run := all[stepOf(all, "publish", "name", "Build and push image")+"run"]; !strings.Contains(run, scan) {
		t.Fatalf("the publish job's build runs\n%s\nwant it to run %s", run, scan)
	}
	for _, copied := range []string{"aquasecurity/trivy", "trivy image", "--ignore-unfixed", "--exit-code"} {
		if strings.Contains(string(raw), copied) {
			t.Errorf("publish-block.yml holds %q, a second copy of what scan-image.sh pins", copied)
		}
	}
	for _, job := range []string{"conformance", "opentofu-conformance"} {
		index := func(key, value string) int {
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(stepOf(all, job, key, value), "jobs."+job+".steps."), "."))
			if err != nil {
				return -1
			}
			return n
		}
		step := func(i int, key string) string { return all["jobs."+job+".steps."+strconv.Itoa(i)+"."+key] }
		kit, moved, goAt := index("with.ref", "${{ inputs.kit-ref }}"), index("run", `mv block-kit "$RUNNER_TEMP/block-kit"`), index("with.go-version", "${{ inputs.go-version }}")
		built, scanned := index("name", "Build the block image"), index("name", "Scan the block image")
		cases := []struct{ name, got, want string }{
			{"the kit comes from kit-ref", step(kit, "with.repository"), "mitstack-eu/block-kit"},
			{"the kit leaves the tree for RUNNER_TEMP before the image builds", strconv.FormatBool(kit >= 0 && kit < moved && moved < built), "true"},
			{"the build tags the image the scan reads", strconv.FormatBool(strings.Contains(step(built, "run"), "-t "+image)), "true"},
			{"Go, which the scan's exception builds with, is set up before the scan", strconv.FormatBool(goAt >= 0 && goAt < scanned), "true"},
			{"the scan reads the image after it is built", strconv.FormatBool(built >= 0 && built < scanned), "true"},
			{"the scan runs on a pull request alone, since publish scans a push", step(scanned, "if"), "github.event_name == 'pull_request'"},
			{"the scan runs the publish job's script", step(scanned, "run"), scan + image},
		}
		for _, tc := range cases {
			t.Run(job+": "+tc.name, func(t *testing.T) {
				if tc.got != tc.want {
					t.Errorf("publish-block.yml field = %q, want %q", tc.got, tc.want)
				}
			})
		}
	}
}

func TestPublishBlockWorkflow_ThePullRequestScanFailsTheCheckNamingARefusedFinding(t *testing.T) {
	for _, job := range []string{"conformance", "opentofu-conformance"} {
		for _, refused := range []string{"", "GO-2026-5932"} {
			t.Run(job+" refusing "+refused, func(t *testing.T) {
				temp := t.TempDir()
				writeFile(t, filepath.Join(temp, "block-kit", "scripts", "scan-image.sh"), "echo \"scanned $1\"\n[ -z \"$REFUSED\" ] || { echo \"refused $REFUSED\"; exit 1; }\n")

				out, exit := runNamed(t, job, "Scan the block image", t.TempDir(), "", "RUNNER_TEMP="+temp, "GITHUB_SHA=c0ffee", "REFUSED="+refused)

				if (exit != 0) != (refused != "") || !strings.Contains(out, "scanned block:c0ffee") || !strings.Contains(out, "refused "+refused) && refused != "" {
					t.Errorf("exit %d, output:\n%s\nwant the scan of block:c0ffee to fail the step exactly when it refuses %q, naming it", exit, out, refused)
				}
			})
		}
	}
}

const (
	onAPushToMain = "github.event_name == 'push' && github.ref == format('refs/heads/{0}', github.event.repository.default_branch)"
	everyCheck    = "[conformance, opentofu-conformance, secrets, scripts]"
)

func TestPublishBlockWorkflow_PublishesOnlyForACallerNamingARegistry(t *testing.T) {
	all := publishWorkflow(t)
	field := func(path string) string {
		if v, ok := all[path]; ok {
			return v
		}
		return "<absent>"
	}
	credentials := stepOf(all, "publish", "with.role-to-assume", "${{ vars.AWS_BLOCK_PUBLISH_ROLE_ARN }}")
	if credentials == "" {
		t.Fatal("publish has no step assuming the role vars.AWS_BLOCK_PUBLISH_ROLE_ARN names")
	}
	cases := []struct{ name, got, want string }{
		{"ecr-repository is optional, so a block repository's ci.yml may name no registry", field("on.workflow_call.inputs.ecr-repository.required"), "false"},
		{"a caller naming no registry gets no publish on a push to main, and a caller naming one and holding its publish role publishes there as it does today",
			field("jobs.publish.if"), "${{ !cancelled() && !failure() && inputs.ecr-repository != '' && " + onAPushToMain + " }}"},
		{"a caller naming a registry still publishes only once every check passed", field("jobs.publish.needs"), everyCheck},
		{"a caller naming a registry with no publish role fails at the credentials step, which carries no if", field(credentials + "if"), "<absent>"},
		{"a caller naming no registry gets the no-registry summary on a push to main where publish would have run",
			field("jobs.no-registry.if"), "${{ !cancelled() && !failure() && inputs.ecr-repository == '' && " + onAPushToMain + " }}"},
		{"the no-registry summary waits for every check, as publish does", field("jobs.no-registry.needs"), everyCheck},
		{"the no-registry summary job is granted nothing, since it only writes the summary", field("jobs.no-registry.permissions"), "{}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("publish-block.yml field = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// A block repository naming no registry still gets its checks, its platform verdict and its attestation, since none of those jobs reads the registry.
func TestPublishBlockWorkflow_ACallerNamingNoRegistryGetsItsChecksAndAttestation(t *testing.T) {
	all := publishWorkflow(t)
	for _, job := range []string{"conformance", "opentofu-conformance", "secrets", "scripts", "platform-verdict", "attest"} {
		t.Run(job, func(t *testing.T) {
			if _, ok := all["jobs."+job]; !ok {
				t.Fatalf("publish-block.yml has no job %s", job)
			}

			for path, v := range all {
				if strings.HasPrefix(path, "jobs."+job+".") && strings.Contains(v, "inputs.ecr-repository") {
					t.Errorf("%s = %q reads the registry, want %s to run whether or not a caller names one", path, v, job)
				}
			}
		})
	}
}

func TestPublishBlockWorkflow_ACallerNamingNoRegistryReadsInTheSummaryThatNothingWasPublished(t *testing.T) {
	summary := filepath.Join(t.TempDir(), "summary.md")

	out, exit := runNamed(t, "no-registry", "Say that no registry was named and nothing was published", t.TempDir(), "", "GITHUB_STEP_SUMMARY="+summary)

	if exit != 0 {
		t.Fatalf("the no-registry step exit %d:\n%s", exit, out)
	}
	got, err := os.ReadFile(summary)
	if want := "No image registry was named in the ecr-repository input, so nothing was published.\n"; err != nil || string(got) != want {
		t.Errorf("the run's summary = %q (err %v), want %q", got, err, want)
	}
}

const (
	kitScript   = `bash "$RUNNER_TEMP/block-kit/scripts/`
	writePRBase = kitScript + `write-pr-base.sh" "$BASE_SHA" "$RUNNER_TEMP"`
)

// stepAt returns the index of job's first step whose key holds value, or -1.
func stepAt(all map[string]string, job, key, value string) int {
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(stepOf(all, job, key, value), "jobs."+job+".steps."), "."))
	if err != nil {
		return -1
	}
	return n
}

// Both conformance jobs hand blockverify a pull request's base manifest and changed files through the kit's two scripts, so the version rule reads one implementation.
func TestPublishBlockWorkflow_HandsBlockverifyTheChangedFiles(t *testing.T) {
	all := publishWorkflow(t)
	jobs := []struct{ job, judge, token string }{
		{"conformance", kitScript + `run-blockverify.sh" ./blockverify blockverify-report.json "$EVENT_NAME" "$RUNNER_TEMP"`, ""},
		{"opentofu-conformance", kitScript + `run-blockverify.sh" "$RUNNER_TEMP/blockverify" "$RUNNER_TEMP/blockverify-report.json" "$EVENT_NAME" "$RUNNER_TEMP" --opentofu-only`, "${{ github.token }}"},
	}
	for _, j := range jobs {
		moved, wrote, judged := stepAt(all, j.job, "run", `mv block-kit "$RUNNER_TEMP/block-kit"`), stepAt(all, j.job, "run", writePRBase), stepAt(all, j.job, "run", j.judge)
		step := func(i int, key string) string { return all["jobs."+j.job+".steps."+strconv.Itoa(i)+"."+key] }
		cases := []struct{ name, got, want string }{
			{"the kit reaches RUNNER_TEMP before the write, and the write comes before blockverify", strconv.FormatBool(moved >= 0 && moved < wrote && wrote < judged), "true"},
			{"the write runs on a pull request alone, since a push has no base commit", step(wrote, "if"), "github.event_name == 'pull_request'"},
			{"blockverify learns the event", step(judged, "env.EVENT_NAME"), "${{ github.event_name }}"},
			{"the fetch takes the job's token only where the checkout keeps none", step(wrote, "env.FETCH_TOKEN"), j.token},
		}
		for _, tc := range cases {
			t.Run(j.job+": "+tc.name, func(t *testing.T) {
				if tc.got != tc.want {
					t.Errorf("publish-block.yml field = %q, want %q", tc.got, tc.want)
				}
			})
		}
	}
}

// The image copies the block's whole tree, so the OpenTofu job keeps blockverify and its report in RUNNER_TEMP and runs it before the verify role is assumed.
func TestPublishBlockWorkflow_TheOpenTofuJobBuildsBlockverifyFromItsKitCheckout(t *testing.T) {
	all := publishWorkflow(t)
	const job = "opentofu-conformance"
	kit, moved := stepAt(all, job, "with.ref", "${{ inputs.kit-ref }}"), stepAt(all, job, "run", `mv block-kit "$RUNNER_TEMP/block-kit"`)
	built := stepAt(all, job, "run", `go build -o "$RUNNER_TEMP/blockverify" ./verify/blockverify`)
	judged, assumed := stepAt(all, job, "name", "blockverify (standalone contract check)"), stepAt(all, job, "name", "Configure AWS credentials for the verify role")
	report := stepOf(all, job, "with.name", "blockverify-report")
	cases := []struct{ name, got, want string }{
		{"blockverify builds from the kit checkout before it leaves the tree", strconv.FormatBool(kit >= 0 && kit < built && built < moved), "true"},
		{"the build runs in the kit checkout", all["jobs."+job+".steps."+strconv.Itoa(built)+".working-directory"], "block-kit"},
		{"blockverify runs before the job assumes the verify role", strconv.FormatBool(judged >= 0 && judged < assumed), "true"},
		{"the report uploads whatever the verdict", all[report+"if"], "always()"},
		{"the report uploads from RUNNER_TEMP", all[report+"with.path"], "${{ runner.temp }}/blockverify-report.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("publish-block.yml field = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestPublishBlockWorkflow_AsksThePlatformOnlyWhereAStarterTurnsItOn(t *testing.T) {
	all := publishWorkflow(t)
	cases := []struct{ name, path, want string }{
		{"platform-verdict is off unless a starter sets it", "on.workflow_call.inputs.platform-verdict.default", "false"},
		{"post-attestation is off unless a starter sets it", "on.workflow_call.inputs.post-attestation.default", "false"},
		{"the platform's address defaults to production", "on.workflow_call.inputs.api-url.default", "https://api.mitstack.dev"},
		{"the sealed token is optional for a caller that turns neither on", "on.workflow_call.secrets.MITSTACK_API_TOKEN.required", "false"},
		{"the verdict runs on a pull request alone", "jobs.platform-verdict.if", "inputs.platform-verdict && github.event_name == 'pull_request'"},
		{"the verdict reads contents alone", "jobs.platform-verdict.permissions.contents", "read"},
		{"the attestation runs on a push to the default branch alone", "jobs.attest.if", "inputs.post-attestation && github.event_name == 'push' && github.ref == format('refs/heads/{0}', github.event.repository.default_branch)"},
		{"the attestation waits for every check", "jobs.attest.needs", "[conformance, secrets, scripts]"},
		{"the attestation reads contents alone", "jobs.attest.permissions.contents", "read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := all[tc.path]; got != tc.want {
				t.Errorf("publish-block.yml %s = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

type platformCall struct{ method, uri, auth, body string }

func fakePlatform(t *testing.T, status int, answer string) (string, *[]platformCall) {
	t.Helper()
	var calls []platformCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, platformCall{r.Method, r.RequestURI, r.Header.Get("Authorization"), string(body)})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

// runNamed runs the run script job names name, in dir, against the platform at apiURL, with override added last to its environment.
func runNamed(t *testing.T, job, name, dir, apiURL string, override ...string) (string, int) {
	t.Helper()
	all := publishWorkflow(t)
	prefix := stepOf(all, job, "name", name)
	if prefix == "" {
		t.Fatalf("%s runs nothing named %q", job, name)
	}
	script := filepath.Join(t.TempDir(), "named.sh")
	if err := os.WriteFile(script, []byte(all[prefix+"run"]), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append([]string{"API_URL=" + apiURL, "MITSTACK_API_TOKEN=mitstack_pat_sealed", "BLOCK_ID=@acme/billing-db"}, override...)
	return runBash(t, env, "-c", `cd "$1" && bash -e "$2"`, "named", dir, script)
}

// A caller that turns a job on without passing the secret, as a fork's pull request does, learns which secret is missing before any call.
func TestPublishBlockWorkflow_EveryPlatformCallFailsNamingAMissingSecret(t *testing.T) {
	targets := []struct{ job, name string }{
		{"platform-verdict", "Ask the platform for its verdict on manifest.yaml"},
		{"attest", "Fetch the platform's manifest"},
		{"attest", "Post the attestation"},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "manifest.yaml"), "spec:\n  version: 1.0.0\n")
			writeFile(t, filepath.Join(dir, "attestation-report.json"), `{"verdict":"pass"}`)
			apiURL, calls := fakePlatform(t, 200, "{}")

			out, exit := runNamed(t, target.job, target.name, dir, apiURL, "MITSTACK_API_TOKEN=")

			if exit == 0 || !strings.Contains(out, "needs the MITSTACK_API_TOKEN secret") {
				t.Errorf("%q exit %d, output:\n%s\nwant a failure naming the missing MITSTACK_API_TOKEN secret", target.name, exit, out)
			}
			if len(*calls) != 0 {
				t.Errorf("platform calls = %+v, want none without a token", *calls)
			}
		})
	}
}

func TestPublishBlockWorkflow_ThePlatformVerdictJobFailsOnARefusalAndPassesAnAcceptance(t *testing.T) {
	const name = "Ask the platform for its verdict on manifest.yaml"
	cases := []struct {
		name     string
		status   int
		answer   string
		wantFail bool
		wantOut  string
	}{
		{"a refusal fails the run and prints the platform's reason", 400, `{"error":"spec.params is not a field"}`, true,
			`::error::The platform refused manifest.yaml with HTTP 400: {"error":"spec.params is not a field"}`},
		{"an acceptance passes and prints the verdict", 200, `{"blockId":"@acme/billing-db","created":false,"dryRun":true}`, false,
			`The platform accepts manifest.yaml: {"blockId":"@acme/billing-db","created":false,"dryRun":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "manifest.yaml"), "spec:\n  version: 1.0.0\n")
			apiURL, calls := fakePlatform(t, tc.status, tc.answer)

			out, exit := runNamed(t, "platform-verdict", name, dir, apiURL)

			if (exit != 0) != tc.wantFail || !strings.Contains(out, tc.wantOut) {
				t.Errorf("exit %d, output:\n%s\nwant failing=%v and output containing %q", exit, out, tc.wantFail, tc.wantOut)
			}
			want := []platformCall{{"POST", "/v1/blocks/apply?dryRun=true", "Bearer mitstack_pat_sealed", "spec:\n  version: 1.0.0\n"}}
			if !slices.Equal(*calls, want) {
				t.Errorf("platform calls = %+v, want %+v", *calls, want)
			}
		})
	}
}

func TestPublishBlockWorkflow_TheAttestationJobFetchesThePlatformsManifestAndPostsTheReport(t *testing.T) {
	cases := []struct {
		name, script, answer, wantFile string
		want                           platformCall
	}{
		{"the fetch writes the platform's manifest over the checked-in one", "Fetch the platform's manifest", "metadata:\n  id: '@acme/billing-db'\n", "manifest.yaml",
			platformCall{"GET", "/v1/blocks/%40acme%2Fbilling-db/manifest", "Bearer mitstack_pat_sealed", ""}},
		{"the post sends the report to the block's attestations", "Post the attestation", `{"claims":{}}`, "attestation.json",
			platformCall{"POST", "/v1/blocks/%40acme%2Fbilling-db/attestations", "Bearer mitstack_pat_sealed", `{"verdict":"pass"}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "manifest.yaml"), "checked in\n")
			writeFile(t, filepath.Join(dir, "attestation-report.json"), `{"verdict":"pass"}`)
			apiURL, calls := fakePlatform(t, 201, tc.answer)

			out, exit := runNamed(t, "attest", tc.script, dir, apiURL)

			if exit != 0 {
				t.Fatalf("%q exit %d:\n%s", tc.script, exit, out)
			}
			if got, _ := os.ReadFile(filepath.Join(dir, tc.wantFile)); string(got) != tc.answer {
				t.Errorf("%s = %q, want the platform's answer %q", tc.wantFile, got, tc.answer)
			}
			if !slices.Equal(*calls, []platformCall{tc.want}) {
				t.Errorf("platform calls = %+v, want %+v", *calls, tc.want)
			}
		})
	}
}

func TestPublishBlockWorkflow_TheAttestationJobFailsOnARefusalNamingIt(t *testing.T) {
	const refusal = `{"error":"this token opens only the manifest, attestations and dry-run apply of block @acme/other"}`
	for _, script := range []string{"Fetch the platform's manifest", "Post the attestation"} {
		t.Run(script, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "attestation-report.json"), `{"verdict":"pass"}`)
			apiURL, _ := fakePlatform(t, 403, refusal)

			out, exit := runNamed(t, "attest", script, dir, apiURL)

			if exit == 0 || !strings.Contains(out, "HTTP 403: "+refusal) {
				t.Errorf("%q exit %d, output:\n%s\nwant a failure naming HTTP 403 and the refusal", script, exit, out)
			}
		})
	}
}

const ghcrLogin = "Log in to GHCR with the job's own token"

// Only the jobs that build the block's image may read a private base image, and each logs in only for a caller that turns ghcr-login on.
func TestPublishBlockWorkflow_ReadsAPrivateGHCRBaseOnlyInTheJobsThatBuildTheImage(t *testing.T) {
	all := publishWorkflow(t)
	builders := map[string]struct{ build, gate string }{
		"conformance":          {"Build the block image", "inputs.ghcr-login && github.event_name == 'pull_request'"},
		"opentofu-conformance": {"Build the block image", "inputs.ghcr-login"},
		"publish":              {"Build and push image", "inputs.ghcr-login && steps.existing.outputs.published == 'false'"},
	}
	cases := []struct{ name, got, want string }{
		{"ghcr-login is a boolean input", all["on.workflow_call.inputs.ghcr-login.type"], "boolean"},
		{"ghcr-login is off unless a caller turns it on", all["on.workflow_call.inputs.ghcr-login.default"], "false"},
		{"the workflow-level ceiling grants no packages permission", fieldOr(all, "permissions.packages"), "<absent>"},
	}
	for _, job := range []string{"conformance", "opentofu-conformance", "secrets", "scripts", "platform-verdict", "attest", "publish", "no-registry"} {
		want := "<absent>"
		if _, ok := builders[job]; ok {
			want = "read"
		}
		cases = append(cases, struct{ name, got, want string }{job + " packages permission", fieldOr(all, "jobs."+job+".permissions.packages"), want})
	}
	for job, b := range builders {
		login, build := stepAt(all, job, "name", ghcrLogin), stepAt(all, job, "name", b.build)
		step := func(key string) string { return fieldOr(all, "jobs."+job+".steps."+strconv.Itoa(login)+"."+key) }
		cases = append(cases,
			struct{ name, got, want string }{job + " logs in before it builds the image", strconv.FormatBool(login >= 0 && login < build), "true"},
			struct{ name, got, want string }{job + " logs in only for a caller that turns ghcr-login on", step("if"), b.gate},
			struct{ name, got, want string }{job + " hands docker the job's own token through the environment", step("env.GHCR_TOKEN"), "${{ github.token }}"},
		)
	}
	for _, after := range []string{"go test", "blockverify (standalone contract check)"} {
		cases = append(cases, struct{ name, got, want string }{"conformance logs in after " + after + " has run the block's code",
			strconv.FormatBool(stepAt(all, "conformance", "name", after) < stepAt(all, "conformance", "name", ghcrLogin)), "true"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("publish-block.yml field = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestPublishBlockWorkflow_TheGHCRLoginSendsTheTokenOnStdinAlone(t *testing.T) {
	for _, job := range []string{"conformance", "opentofu-conformance", "publish"} {
		t.Run(job, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "bin", "docker"), "#!/bin/sh\necho \"$@\" > \"$PWD/argv\"\ncat > \"$PWD/stdin\"\n")

			out, exit := runNamed(t, job, ghcrLogin, dir, "", "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"),
				"GHCR_TOKEN=ghs_jobtoken", "GITHUB_ACTOR=block-bot")

			argv, _ := os.ReadFile(filepath.Join(dir, "argv"))
			stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
			if exit != 0 || string(argv) != "login ghcr.io --username block-bot --password-stdin\n" || string(stdin) != "ghs_jobtoken" {
				t.Errorf("exit %d, output %q, docker argv %q, stdin %q; want docker login ghcr.io as the actor with the token on stdin alone", exit, out, argv, stdin)
			}
		})
	}
}

func fieldOr(all map[string]string, path string) string {
	if v, ok := all[path]; ok {
		return v
	}
	return "<absent>"
}
