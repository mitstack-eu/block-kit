package scripts_test

// publish-block.yml runs the kit's scan scripts on every event before publish, hands blockverify what a pull request changes, and asks the platform for its verdict and its attestation where a starter turns them on.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestPublishBlockWorkflow_HandsBlockverifyTheChangedFiles(t *testing.T) {
	all := publishWorkflow(t)
	const flag = `--changed-files "$RUNNER_TEMP/changed-files"`

	run := all[stepOf(all, "conformance", "name", "blockverify (standalone contract check)")+"run"]

	if !strings.Contains(run, flag) {
		t.Errorf("the blockverify step's run =\n%s\nwant it to pass %s", run, flag)
	}
}

func TestPublishBlockWorkflow_ThePullRequestStepListsEveryFileTheMergeChanges(t *testing.T) {
	const name = "Write the base commit's manifest and the changed files"
	prefix := stepOf(publishWorkflow(t), "conformance", "name", name)
	if prefix == "" {
		t.Fatalf("conformance has no step %q", name)
	}
	repo, shas := gitRepo(t,
		change{write: map[string]string{"manifest.yaml": "spec:\n  version: 1.0.0\n", "README.md": "# block\n", "resource.go": "package main\n", "old.go": "package main\n"}},
		change{write: map[string]string{"README.md": "# block, changed\n", "resource.go": "package main\n\nfunc changed() {}\n", "new.go": "package main\n"}, remove: []string{"old.go"}})
	git(t, repo, "remote", "add", "origin", "file://"+repo)
	temp := t.TempDir()
	step := filepath.Join(temp, "step.sh")
	if err := os.WriteFile(step, []byte(publishWorkflow(t)[prefix+"run"]), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-e", step)
	cmd.Dir, cmd.Env = repo, append(hermeticEnv(), "BASE_SHA="+shas[0], "RUNNER_TEMP="+temp)

	out, err := cmd.CombinedOutput()

	if err != nil {
		t.Fatalf("step %q: %v\n%s", name, err, out)
	}
	changed, err := os.ReadFile(filepath.Join(temp, "changed-files"))
	if want := "README.md\x00new.go\x00old.go\x00resource.go\x00"; err != nil || string(changed) != want {
		t.Errorf("changed-files = %q (err %v), want %q, each path NUL-terminated and a moved file under both paths", changed, err, want)
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
