package scripts_test

// publish-block.yml keeps no token in any checkout, runs the kit's scan scripts on every event before publish, scans a pull request's image with the publish job's own scan, publishes only for a caller naming its image registry, hands blockverify in either conformance job what a pull request changes, asks the platform for its verdict and its attestation where a starter turns them on, and tags each release it builds at the commit that built it.

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func publishWorkflowText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../.github/workflows/publish-block.yml")
	if err != nil {
		t.Fatalf("reading publish-block.yml: %v", err)
	}
	return string(raw)
}

func publishWorkflow(t *testing.T) map[string]string {
	t.Helper()
	return workflowFields(publishWorkflowText(t))
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
		{"the secrets job fails a block whose shipped files fetch a tool from a release page", step("secrets", "run", "bash block-kit/scripts/check-runtime-downloads.sh block"), "true"},
		{"the scripts job checks the block out beside the kit", step("scripts", "with.path", "block"), "true"},
		{"the scripts job takes its lint from kit-ref", step("scripts", "with.ref", "${{ inputs.kit-ref }}"), "true"},
		{"the scripts job lints the block with the kit's script", step("scripts", "run", "bash block-kit/scripts/lint-scripts.sh block"), "true"},
	}
	checkFields(t, "publish-block.yml", "", cases)
}

func TestPublishBlockWorkflow_APullRequestRunsThePublishScan(t *testing.T) {
	const scan, image = `bash "$RUNNER_TEMP/block-kit/scripts/scan-image.sh" `, `"block:${GITHUB_SHA}"`
	raw := publishWorkflowText(t)
	all := workflowFields(raw)
	if run := all[stepOf(all, "publish", "name", "Scan and push the image")+"run"]; !strings.Contains(run, scan) {
		t.Fatalf("the publish job's push runs\n%s\nwant it to run %s", run, scan)
	}
	for _, copied := range []string{"aquasecurity/trivy", "trivy image", "--ignore-unfixed", "--exit-code"} {
		if strings.Contains(raw, copied) {
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
		checkFields(t, "publish-block.yml", job+": ", cases)
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
	checkFields(t, "publish-block.yml", "", cases)
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

// Both conformance jobs hand blockverify a pull request's base manifest and changed files through the kit's two scripts, so the version rule reads one implementation, and the platform's address, so the type snapshot is compared on every event.
func TestPublishBlockWorkflow_HandsBlockverifyTheChangedFilesAndThePlatformsAddress(t *testing.T) {
	all := publishWorkflow(t)
	jobs := []struct{ job, judge string }{
		{"conformance", kitScript + `run-blockverify.sh" ./blockverify blockverify-report.json "$EVENT_NAME" "$RUNNER_TEMP" --api-url "$API_URL"`},
		{"opentofu-conformance", kitScript + `run-blockverify.sh" "$RUNNER_TEMP/blockverify" "$RUNNER_TEMP/blockverify-report.json" "$EVENT_NAME" "$RUNNER_TEMP" --opentofu-only --api-url "$API_URL"`},
	}
	for _, j := range jobs {
		moved, wrote, judged, polled := stepAt(all, j.job, "run", `mv block-kit "$RUNNER_TEMP/block-kit"`), stepAt(all, j.job, "run", writePRBase), stepAt(all, j.job, "run", j.judge), stepAt(all, j.job, "run", `"$RUNNER_TEMP/checkpolls" -base "$BASE_SHA"`)
		step := func(i int, key string) string { return all["jobs."+j.job+".steps."+strconv.Itoa(i)+"."+key] }
		cases := []struct{ name, got, want string }{
			{"the kit reaches RUNNER_TEMP before the write, the write comes before blockverify and checkpolls, and checkpolls, built from the kit checkout, judges a pull request alone against its base", strconv.FormatBool(moved >= 0 && moved < wrote && wrote < judged && wrote < polled && stepWith(all, j.job, "run", `go build -o "$RUNNER_TEMP/checkpolls" ./cmd/checkpolls`)) + " " + step(polled, "if") + " " + step(polled, "env.BASE_SHA"), "true github.event_name == 'pull_request' ${{ github.event.pull_request.base.sha }}"},
			{"the write runs on a pull request alone, since a push has no base commit", step(wrote, "if"), "github.event_name == 'pull_request'"},
			{"blockverify learns the event and the platform's address", step(judged, "env.EVENT_NAME") + " " + step(judged, "env.API_URL"), "${{ github.event_name }} ${{ inputs.api-url }}"},
			{"the fetch takes the job's token, since the checkout keeps none", step(wrote, "env.FETCH_TOKEN"), "${{ github.token }}"},
		}
		checkFields(t, "publish-block.yml", j.job+": ", cases)
	}
}

// No job pushes to git, so no checkout writes the job's token into the tree where the block's own code and tests run.
func TestPublishBlockWorkflow_EveryCheckoutKeepsNoToken(t *testing.T) {
	raw := publishWorkflowText(t)
	all := workflowFields(raw)
	checkouts := 0
	for _, key := range slices.Sorted(maps.Keys(all)) {
		step, isUses := strings.CutSuffix(key, ".uses")
		if !isUses || !strings.Contains(step, ".steps.") || !strings.HasPrefix(all[key], "actions/checkout@") {
			continue
		}
		checkouts++
		if got := all[step+".with.persist-credentials"]; got != "false" {
			t.Errorf("checkout %s, named %q, sets persist-credentials %q, want false", step, all[step+".name"], got)
		}
	}
	if want := strings.Count(raw, "uses: actions/checkout@"); checkouts != want {
		t.Errorf("the test read %d checkout steps, but publish-block.yml has %d, so it missed some", checkouts, want)
	}
}

// The image copies the block's whole tree, so the OpenTofu job keeps blockverify and its report in RUNNER_TEMP, and it assumes no role, since the base image job pulls the operator image for it.
func TestPublishBlockWorkflow_TheOpenTofuJobBuildsBlockverifyFromItsKitCheckout(t *testing.T) {
	all := publishWorkflow(t)
	const job = "opentofu-conformance"
	kit, moved := stepAt(all, job, "with.ref", "${{ inputs.kit-ref }}"), stepAt(all, job, "run", `mv block-kit "$RUNNER_TEMP/block-kit"`)
	built := stepAt(all, job, "run", `go build -o "$RUNNER_TEMP/blockverify" ./verify/blockverify`)
	judged, fetched := stepAt(all, job, "name", "blockverify (standalone contract check)"), stepAt(all, job, "name", "Fetch the base images")
	report := stepOf(all, job, "with.name", "blockverify-report")
	cases := []struct{ name, got, want string }{
		{"blockverify builds from the kit checkout before it leaves the tree", strconv.FormatBool(kit >= 0 && kit < built && built < moved), "true"},
		{"the build runs in the kit checkout", all["jobs."+job+".steps."+strconv.Itoa(built)+".working-directory"], "block-kit"},
		{"blockverify runs before the job fetches the base images", strconv.FormatBool(judged >= 0 && judged < fetched), "true"},
		{"the job fetches the base images the base image job uploads, into the directory its build reads", all[stepOf(all, job, "name", "Fetch the base images")+"with.name"] + " " + all[stepOf(all, job, "name", "Fetch the base images")+"with.path"], "base-images ${{ runner.temp }}/base-images"},
		{"the base image job uploads that directory under that name", all[stepOf(all, "base-image", "name", "Hand the base images to the conformance job")+"with.name"] + " " + all[stepOf(all, "base-image", "name", "Hand the base images to the conformance job")+"with.path"], "base-images ${{ runner.temp }}/base-images"},
		{"a rerun of the base image job replaces the images an earlier attempt handed over", all[stepOf(all, "base-image", "name", "Hand the base images to the conformance job")+"with.overwrite"], "true"},
		{"the job assumes no AWS role", strconv.FormatBool(stepWith(all, job, "with.role-to-assume", "${{ vars.AWS_BLOCK_VERIFY_ROLE_ARN }}")), "false"},
		{"the report uploads whatever the verdict", all[report+"if"], "always()"},
		{"the report uploads from RUNNER_TEMP", all[report+"with.path"], "${{ runner.temp }}/blockverify-report.json"},
	}
	checkFields(t, "publish-block.yml", "", cases)
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

// A caller whose organisation defines no board address runs the three checks it ran before, and the delivery job reports as skipped.
func TestPublishBlockWorkflow_JudgesDeliveryOnlyForACallerWithABoard(t *testing.T) {
	all := publishWorkflow(t)
	run := all[stepOf(all, "delivery", "name", "Judge the commit order and the board keys")+"run"]
	env := func(name string) string {
		return all[stepOf(all, "delivery", "name", "Judge the commit order and the board keys")+"env."+name]
	}
	cases := []struct{ name, got, want string }{
		{"the job runs on a pull request from a caller defining TRACKER_BASE_URL", all["jobs.delivery.if"], "github.event_name == 'pull_request' && vars.TRACKER_BASE_URL != ''"},
		{"the job reads contents alone", all["jobs.delivery.permissions.contents"], "read"},
		{"the board token is optional for a caller with no board", all["on.workflow_call.secrets.TRACKER_API_TOKEN.required"], "false"},
		{"the job checks out the full history the commit range needs", strconv.FormatBool(stepWith(all, "delivery", "with.fetch-depth", "0")), "true"},
		{"the job builds the check with the caller's Go", strconv.FormatBool(stepWith(all, "delivery", "with.go-version", "${{ inputs.go-version }}")), "true"},
		{"the job runs plain-delivery's pr-delivery at the release ci.yml runs, leaving a missing token to it", run, prDeliveryRun},
		{"the job reads the range and the title from the pull request event", env("BASE_SHA") + " " + env("HEAD_SHA") + " " + env("PR_TITLE"),
			"${{ github.event.pull_request.base.sha }} ${{ github.event.pull_request.head.sha }} ${{ github.event.pull_request.title }}"},
		{"the job reads the author's login from the pull request event", env("PR_AUTHOR"), "${{ github.event.pull_request.user.login }}"},
		{"the job reads the board with the caller's address and token", env("TRACKER_BASE_URL") + " " + env("TRACKER_API_TOKEN"), "${{ vars.TRACKER_BASE_URL }} ${{ secrets.TRACKER_API_TOKEN }}"},
		{"the secrets job still runs on every event", all["jobs.secrets.if"], ""},
		{"the scripts job still runs on every event", all["jobs.scripts.if"], ""},
		{"the Go conformance job still runs for every Go block", all["jobs.conformance.if"], "${{ !inputs.opentofu-only }}"},
	}
	checkFields(t, "publish-block.yml", "", cases)
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

// releaseSteps are the publish job's steps that guard, cut and announce a release tag, in the order they run.
var releaseSteps = []string{"Refuse a version tagged at another commit", "Tag the release at the commit it built", "Start the block's publisher at the release tag"}

// A release tag is cut only by a run that built its image or completes its record from this commit, after both are pushed, so a tag never names a commit that built nothing.
func TestPublishBlockWorkflow_TagsAReleaseOnlyFromTheCommitThatBuiltItsImage(t *testing.T) {
	all := publishWorkflow(t)
	at := func(name string) int { return stepAt(all, "publish", "name", name) }
	step := func(name, key string) string { return all[stepOf(all, "publish", "name", name)+key] }
	probe, built, recorded := at("Probe the version tag and its provenance record"), at("Scan and push the image"), at("blockprov push")
	refused, tagged, started := at(releaseSteps[0]), at(releaseSteps[1]), at(releaseSteps[2])
	cases := []struct{ name, got, want string }{
		{"the publish job may push the tag", all["jobs.publish.permissions.contents"], "write"},
		{"the publish job may start the publisher", all["jobs.publish.permissions.actions"], "write"},
		{"the publish job still takes its identity token", all["jobs.publish.permissions.id-token"], "write"},
		{"the publisher a caller names is optional", all["on.workflow_call.inputs.publisher-workflow.required"], "false"},
		{"the publisher defaults to release-publisher.yml", all["on.workflow_call.inputs.publisher-workflow.default"], "release-publisher.yml"},
		{"the refusal runs after the probe and before the image push", strconv.FormatBool(probe >= 0 && probe < refused && refused < built), "true"},
		{"the tag is cut after the image and its record are pushed", strconv.FormatBool(built < recorded && recorded < tagged), "true"},
		{"the publisher starts after the tag exists", strconv.FormatBool(tagged >= 0 && tagged < started), "true"},
		{"the publisher learns the workflow the caller names", step(releaseSteps[2], "env.PUBLISHER_WORKFLOW"), "${{ inputs.publisher-workflow }}"},
	}
	for _, name := range releaseSteps {
		cases = append(cases,
			struct{ name, got, want string }{name + " runs only where this commit built the image", step(name, "if"), "steps.existing.outputs.built == 'true'"},
			struct{ name, got, want string }{name + " reads the tag the probe named", step(name, "env.TAG"), "${{ steps.existing.outputs.tag }}"},
			struct{ name, got, want string }{name + " calls GitHub with the job's token, never a checkout's", step(name, "env.GITHUB_TOKEN"), "${{ github.token }}"})
	}
	checkFields(t, "publish-block.yml", "", cases)

	for _, job := range []string{"conformance", "base-image", "opentofu-conformance", "secrets", "scripts", "platform-verdict", "attest"} {
		if got := all["jobs."+job+".permissions.contents"]; got != "read" {
			t.Errorf("publish-block.yml jobs.%s.permissions.contents = %q, want read, since only publish pushes a tag", job, got)
		}
	}
}

// githubAPI stands in for the GitHub REST API the release steps call, holding each tag's commit and logging every call.
type githubAPI struct {
	url   string
	tags  map[string]string
	calls []string
}

const (
	jobToken     = "ghs_job"
	annotatedTag = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
)

// fakeGitHub serves the tags in tags, a tag object at annotatedTag naming the commit tags holds under "annotated", and refuses every call with refuse when it is set.
func fakeGitHub(t *testing.T, tags map[string]string, refuse int) *githubAPI {
	t.Helper()
	g := &githubAPI{tags: map[string]string{}}
	maps.Copy(g.tags, tags)
	const repo = "/repos/acme/billing-db/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.calls = append(g.calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		path, inRepo := strings.CutPrefix(r.URL.Path, repo)
		tag, isRef := strings.CutPrefix(path, "git/ref/tags/")
		switch {
		case r.Header.Get("Authorization") != "Bearer "+jobToken || !inRepo:
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		case refuse != 0:
			http.Error(w, `{"message":"Server Error"}`, refuse)
		case r.Method == http.MethodGet && isRef && tag == "annotated":
			_, _ = io.WriteString(w, `{"object":{"type":"tag","sha":"`+annotatedTag+`"}}`)
		case r.Method == http.MethodGet && isRef && g.tags[tag] != "":
			_, _ = io.WriteString(w, `{"object":{"type":"commit","sha":"`+g.tags[tag]+`"}}`)
		case r.Method == http.MethodGet && isRef:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case r.Method == http.MethodGet && path == "git/tags/"+annotatedTag:
			_, _ = io.WriteString(w, `{"object":{"type":"commit","sha":"`+g.tags["annotated"]+`"}}`)
		case r.Method == http.MethodPost && path == "git/refs":
			var ref struct{ Ref, Sha string }
			if err := json.Unmarshal(body, &ref); err != nil {
				http.Error(w, `{"message":"Problems parsing JSON"}`, http.StatusBadRequest)
				return
			}
			g.tags[strings.TrimPrefix(ref.Ref, "refs/tags/")] = ref.Sha
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && path == "actions/workflows/release-publisher.yml/dispatches":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, `{"message":"the stand-in does not answer this call"}`, http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	g.url = srv.URL
	return g
}

const (
	builtCommit = "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff"
	laterCommit = "decafdecafdecafdecafdecafdecafdecafdecaf"
)

// Each release step runs the kit's own script from RUNNER_TEMP, so the cases copy it there and answer GitHub through the stand-in.
func TestPublishBlockWorkflow_TheReleaseStepsCutOneTagThatNeverMoves(t *testing.T) {
	const tag = "v1.2.0"
	read := "GET /repos/acme/billing-db/git/ref/tags/" + tag
	cut := "POST /repos/acme/billing-db/git/refs " + `{"ref":"refs/tags/` + tag + `","sha":"` + builtCommit + `"}`
	start := "POST /repos/acme/billing-db/actions/workflows/release-publisher.yml/dispatches " + `{"ref":"` + tag + `","inputs":{"tag":"` + tag + `"}}`
	cases := []struct {
		name, step string
		tags       map[string]string
		publisher  bool
		refuse     int
		wantFail   string
		wantCalls  []string
		wantTagAt  string
	}{
		{"the refusal passes a version with no tag yet", releaseSteps[0], nil, false, 0, "", []string{read}, ""},
		{"the refusal passes a version already tagged at this commit", releaseSteps[0], map[string]string{tag: builtCommit}, false, 0, "", []string{read}, builtCommit},
		{"the refusal fails a version tagged at another commit, naming the tag and both commits", releaseSteps[0], map[string]string{tag: laterCommit}, false, 0,
			tag + " already names commit " + laterCommit + ", and this run built commit " + builtCommit, []string{read}, laterCommit},
		{"the refusal fails when GitHub cannot say whether the tag exists, naming its answer", releaseSteps[0], nil, false, http.StatusBadGateway,
			"GitHub answered HTTP 502", []string{read}, ""},
		{"the tag step cuts the tag at this commit", releaseSteps[1], nil, false, 0, "", []string{read, cut}, builtCommit},
		{"the tag step on a rerun finds the tag at this commit and pushes nothing", releaseSteps[1], map[string]string{tag: builtCommit}, false, 0, "", []string{read}, builtCommit},
		{"the tag step never moves a tag another commit holds", releaseSteps[1], map[string]string{tag: laterCommit}, false, 0,
			tag + " already names commit " + laterCommit, []string{read}, laterCommit},
		{"a block holding the publisher starts it at the tag", releaseSteps[2], map[string]string{tag: builtCommit}, true, 0, "", []string{start}, builtCommit},
		{"a publisher GitHub refuses to start fails the step, naming its answer", releaseSteps[2], map[string]string{tag: builtCommit}, true, http.StatusInternalServerError,
			"could not start release-publisher.yml at " + tag + ": GitHub answered HTTP 500", []string{start}, builtCommit},
		{"a block holding no publisher starts nothing and passes", releaseSteps[2], map[string]string{tag: builtCommit}, false, 0, "", nil, builtCommit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, exit, github := runReleaseStep(t, tc.step, tag, tc.tags, tc.publisher, tc.refuse)
			if (exit != 0) != (tc.wantFail != "") || !strings.Contains(out, tc.wantFail) {
				t.Errorf("%q exit %d, output:\n%s\nwant failing=%v naming %q", tc.step, exit, out, tc.wantFail != "", tc.wantFail)
			}
			if !slices.Equal(github.calls, tc.wantCalls) {
				t.Errorf("GitHub calls = %q, want %q", github.calls, tc.wantCalls)
			}
			if got := github.tags[tag]; got != tc.wantTagAt {
				t.Errorf("%s names commit %q after the step, want %q", tag, got, tc.wantTagAt)
			}
		})
	}
}

// A tag someone cut by hand as an annotated tag names a tag object, so the refusal reads the commit that object names.
func TestPublishBlockWorkflow_TheRefusalReadsTheCommitAnAnnotatedTagNames(t *testing.T) {
	for _, tc := range []struct {
		name, commit string
		wantExit     int
	}{
		{"an annotated tag at this commit passes", builtCommit, 0},
		{"an annotated tag at another commit fails", laterCommit, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, exit, github := runReleaseStep(t, releaseSteps[0], "annotated", map[string]string{"annotated": tc.commit}, false, 0)
			if exit != tc.wantExit || (exit != 0 && !strings.Contains(out, "already names commit "+tc.commit)) {
				t.Errorf("exit %d, output:\n%s\nwant exit %d naming commit %s", exit, out, tc.wantExit, tc.commit)
			}
			want := []string{"GET /repos/acme/billing-db/git/ref/tags/annotated", "GET /repos/acme/billing-db/git/tags/" + annotatedTag}
			if !slices.Equal(github.calls, want) {
				t.Errorf("GitHub calls = %q, want %q", github.calls, want)
			}
		})
	}
}

// runReleaseStep runs the publish step named step for tag against a GitHub holding tags, with the kit's release-tag.sh copied where the job keeps the kit.
func runReleaseStep(t *testing.T, step, tag string, tags map[string]string, publisher bool, refuse int) (string, int, *githubAPI) {
	t.Helper()
	dir, temp := t.TempDir(), t.TempDir()
	script, err := os.ReadFile("release-tag.sh")
	if err != nil {
		t.Fatalf("reading the kit's release-tag.sh: %v", err)
	}
	writeFile(t, filepath.Join(temp, "block-kit", "scripts", "release-tag.sh"), string(script))
	if publisher {
		writeFile(t, filepath.Join(dir, ".github", "workflows", "release-publisher.yml"), "name: release-publisher\n")
	}
	github := fakeGitHub(t, tags, refuse)
	out, exit := runNamed(t, "publish", step, dir, "", "RUNNER_TEMP="+temp, "TAG="+tag, "GITHUB_SHA="+builtCommit, "GITHUB_API_URL="+github.url,
		"GITHUB_REPOSITORY=acme/billing-db", "GITHUB_TOKEN="+jobToken, "PUBLISHER_WORKFLOW=release-publisher.yml")
	return out, exit, github
}

// Each job that builds the block's image reads packages and logs in to ghcr.io with its own token before its first build, the conformance job before the block's tests, and no other job reads packages, publish included, since it pushes the image a conformance job built.
func TestPublishBlockWorkflow_OnlyTheJobsThatBuildTheImageReadTheBlocksOwnPackages(t *testing.T) {
	all := publishWorkflow(t)
	builders := map[string]string{"conformance": "go test", "opentofu-conformance": "Build the block image"}

	for _, job := range slices.Sorted(maps.Keys(all)) {
		job, isJob := strings.CutPrefix(job, "jobs.")
		if !isJob || strings.Contains(job, ".") {
			continue
		}
		firstBuild, builds := builders[job]
		if !builds {
			if got, ok := all["jobs."+job+".permissions.packages"]; ok {
				t.Errorf("publish-block.yml jobs.%s.permissions.packages = %q, want it absent", job, got)
			}
			continue
		}
		login := stepAt(all, job, "run", `printf '%s' "$GHCR_TOKEN" | docker login ghcr.io --username "$GHCR_USER" --password-stdin`)
		step := func(key string) string { return all["jobs."+job+".steps."+strconv.Itoa(login)+"."+key] }
		checkFields(t, "publish-block.yml", job+": ", []struct{ name, got, want string }{
			{"the job may read packages", all["jobs."+job+".permissions.packages"], "read"},
			{"the login takes the job's own token and actor", step("env.GHCR_TOKEN") + " " + step("env.GHCR_USER"), "${{ github.token }} ${{ github.actor }}"},
			{"the login comes before the job's first build", strconv.FormatBool(login >= 0 && login < stepAt(all, job, "name", firstBuild)), "true"},
		})
	}
}

// GitHub refuses to start a called job that asks for more than its caller grants, so every caller the docs show grants packages: read.
func TestBlockChecksDoc_EveryCallerExampleGrantsPackagesRead(t *testing.T) {
	raw, err := os.ReadFile("../docs/block-checks.md")
	if err != nil {
		t.Fatalf("reading docs/block-checks.md: %v", err)
	}
	callers := 0

	for _, fence := range strings.Split(string(raw), "```yaml")[1:] {
		example, _, _ := strings.Cut(fence, "```")
		all := workflowFields(example)
		if strings.HasPrefix(all["jobs.block.uses"], "mitstack-eu/block-kit/.github/workflows/publish-block.yml@") {
			callers++
			if got := all["jobs.block.permissions.packages"]; got != "read" {
				t.Errorf("the caller example using %s grants packages %q, want read", all["jobs.block.uses"], got)
			}
		}
	}

	if callers == 0 {
		t.Fatal("docs/block-checks.md shows no caller of publish-block.yml, so the check read nothing")
	}
}
