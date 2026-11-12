package scripts_test

// base-images.sh pull reads the Dockerfile through GitHub's API, pulls and saves every image it names in the registry and lists them, and load loads them and prints one build context per image.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	handoverDocker = `#!/usr/bin/env bash
echo "docker $*" >> "$STUB_LOG/docker"
[ "$1" != "${STUB_FAIL:-}" ] || exit 1
while [ $# -gt 0 ]; do
  case "$1" in
    --output) echo "saved image" > "$2" ;;
    --iidfile) echo "sha256:feed" > "$2" ;;
  esac
  shift
done
`
	verifyRegistry = "123456789012.dkr.ecr.eu-central-1.amazonaws.com"
	headCommit     = "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff"
)

var operatorImage = verifyRegistry + "/mitstack-opentofu-operator@sha256:" + strings.Repeat("ab", 32)

func stubbedDocker(t *testing.T) (env []string, log string) {
	t.Helper()
	bin, log := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(bin, "docker"), handoverDocker)
	return []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "STUB_LOG=" + log}, log
}

func dockerCalls(log string) string {
	raw, _ := os.ReadFile(filepath.Join(log, "docker"))
	return strings.TrimSpace(string(raw))
}

func dockerfileAPI(t *testing.T, status int, dockerfile string) (url string, calls *[]string) {
	t.Helper()
	calls = new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization")+" "+r.Header.Get("Accept"))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(dockerfile))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, calls
}

func TestBaseImages_PullSavesEveryImageTheDockerfileNamesInTheRegistry(t *testing.T) {
	const other = verifyRegistry + "/mitstack-other-operator:1.2.0"
	cases := []struct {
		name, dockerfile, fail string
		status                 int
		wantExit               bool
		wantOut                string
		wantPulled             []string
	}{
		{"an image an ARG pins by digest is pulled, tagged and saved", "ARG OPERATOR_IMAGE=" + operatorImage + "\nFROM ${OPERATOR_IMAGE}\nCOPY . /module/\n", "", 200, true, "", []string{operatorImage}},
		{"each image in the registry is pulled once, and an image elsewhere is left to the build", "FROM " + other + " AS tools\nFROM docker.io/library/alpine:3.20\nFROM " + operatorImage + "\nCOPY --from=" + other + " /x /x\n", "", 200, true, "", []string{operatorImage, other}},
		{"an image only a comment names is not pulled", "  # was " + other + "\nARG OPERATOR_IMAGE=\"" + operatorImage + "\"\nFROM ${OPERATOR_IMAGE}\n", "", 200, true, "", []string{operatorImage}},
		{"a Dockerfile naming no image in the registry warns, hands over an empty list and saves nothing", "FROM docker.io/library/alpine:3.20\n", "", 200, true, "::warning::the Dockerfile of acme/bucket names no image in " + verifyRegistry, nil},
		{"a name a variable completes fails naming it, since the build would ask for another name, and nothing is pulled", "ARG OPERATOR_VERSION=1.2.0\nFROM " + verifyRegistry + "/mitstack-opentofu-operator:${OPERATOR_VERSION}\n", "", 200, false,
			"::error::the Dockerfile of acme/bucket names " + verifyRegistry + "/mitstack-opentofu-operator:${OPERATOR_VERSION}", nil},
		{"a Dockerfile GitHub will not serve fails naming its answer, and nothing is pulled", `{"message":"Not Found"}`, "", 404, false, "GitHub answered HTTP 404: " + `{"message":"Not Found"}`, nil},
		{"an image that cannot be pulled fails the job, and nothing is saved", "FROM " + operatorImage + "\n", "pull", 200, false, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, log := stubbedDocker(t)
			api, calls := dockerfileAPI(t, tc.status, tc.dockerfile)
			dir := filepath.Join(t.TempDir(), "base-images")

			out, exit := runBash(t, append(env, "STUB_FAIL="+tc.fail, "GITHUB_API_URL="+api, "GITHUB_REPOSITORY=acme/bucket", "GITHUB_SHA="+headCommit, "GITHUB_TOKEN="+jobToken),
				"base-images.sh", "pull", verifyRegistry, dir)

			if (exit == 0) != tc.wantExit || !strings.Contains(out, tc.wantOut) {
				t.Fatalf("exit %d, output:\n%s\nwant success %v and output naming %q", exit, out, tc.wantExit, tc.wantOut)
			}
			if want := []string{"GET /repos/acme/bucket/contents/Dockerfile?ref=" + headCommit + " Bearer " + jobToken + " application/vnd.github.raw+json"}; strings.Join(*calls, "\n") != strings.Join(want, "\n") {
				t.Errorf("GitHub calls = %q, want %q", *calls, want)
			}
			var wantDocker, wantList, tags []string
			for i, ref := range tc.wantPulled {
				tag := "base-image:" + string(rune('0'+i))
				wantDocker = append(wantDocker, "docker pull "+ref, "docker tag "+ref+" "+tag)
				wantList = append(wantList, ref+" "+tag+"\n")
				tags = append(tags, tag)
			}
			if len(tags) > 0 {
				wantDocker = append(wantDocker, "docker save --output "+filepath.Join(dir, "images.tar")+" "+strings.Join(tags, " "))
			}
			if tc.fail != "" {
				wantDocker = []string{"docker pull " + operatorImage}
			}
			if got := dockerCalls(log); got != strings.Join(wantDocker, "\n") {
				t.Errorf("docker calls =\n%s\nwant\n%s", got, strings.Join(wantDocker, "\n"))
			}
			if got, err := os.ReadFile(filepath.Join(dir, "references")); tc.wantExit && (err != nil || string(got) != strings.Join(wantList, "")) {
				t.Errorf("references = %q (err %v), want %q", got, err, strings.Join(wantList, ""))
			}
			if _, err := os.Stat(filepath.Join(dir, "images.tar")); (err == nil) != (len(tags) > 0) {
				t.Errorf("images.tar stat err = %v, want it saved exactly when an image was pulled", err)
			}
		})
	}
}

func TestBaseImages_LoadPrintsOneBuildContextForEachImageHandedOver(t *testing.T) {
	cases := []struct {
		name       string
		references *string
		fail       string
		wantExit   bool
		wantOut    string
		wantDocker string
	}{
		{"an image handed over is loaded and named as the build context for its reference", ptr(operatorImage + " base-image:0\n"), "", true, "--build-context\n" + operatorImage + "=docker-image://base-image:0\n", "docker load --input DIR/images.tar"},
		{"an empty list loads nothing and prints nothing", ptr(""), "", true, "", ""},
		{"a directory holding no list fails, since the base image job handed nothing over", nil, "", false, "holds no references file", ""},
		{"images that cannot be loaded fail the step and name no build context", ptr(operatorImage + " base-image:0\n"), "load", false, "", "docker load --input DIR/images.tar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, log := stubbedDocker(t)
			dir := t.TempDir()
			if tc.references != nil {
				writeFile(t, filepath.Join(dir, "references"), *tc.references)
				writeFile(t, filepath.Join(dir, "images.tar"), "saved image\n")
			}

			out, exit := runBash(t, append(env, "STUB_FAIL="+tc.fail), "base-images.sh", "load", dir)

			if (exit == 0) != tc.wantExit {
				t.Fatalf("exit %d, output:\n%s\nwant success %v", exit, out, tc.wantExit)
			}
			if tc.wantExit && out != tc.wantOut || !tc.wantExit && (!strings.Contains(out, tc.wantOut) || strings.Contains(out, "--build-context")) {
				t.Errorf("output = %q, want %q and, on a failure, no build context", out, tc.wantOut)
			}
			if got := strings.ReplaceAll(dockerCalls(log), dir, "DIR"); got != tc.wantDocker {
				t.Errorf("docker calls = %q, want %q", got, tc.wantDocker)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// A rerun whose base image job fails could otherwise build on the images an earlier attempt handed over.
func TestPublishBlockWorkflow_TheOpenTofuJobStopsWhereTheBaseImageJobDidNotSucceed(t *testing.T) {
	all := publishWorkflow(t)
	const job, name = "opentofu-conformance", "Stop where the base image job did not succeed"
	stop := stepOf(all, job, "name", name)
	cases := []struct{ name, got, want string }{
		{"the stop comes before the fetch of the base images", strconv.FormatBool(stepAt(all, job, "name", name) >= 0 && stepAt(all, job, "name", name) < stepAt(all, job, "name", "Fetch the base images")), "true"},
		{"the stop runs for every result but success", all[stop+"if"], "needs.base-image.result != 'success'"},
		{"the stop learns how the base image job ended", all[stop+"env.RESULT"], "${{ needs.base-image.result }}"},
	}
	checkFields(t, "publish-block.yml", "", cases)

	out, exit := runNamed(t, job, name, t.TempDir(), "", "RESULT=failure")

	if exit == 0 || !strings.Contains(out, "::error::the base image job ended failure") {
		t.Errorf("exit %d, output:\n%s\nwant the step to fail naming how the base image job ended", exit, out)
	}
}

func TestPublishBlockWorkflow_TheOpenTofuJobBuildsOnTheBaseImagesItWasHanded(t *testing.T) {
	for _, tc := range []struct {
		name, fail string
		wantExit   bool
		wantDocker string
		wantOutput string
	}{
		{"the build reads each handed-over image as the context for its reference", "", true,
			"docker load --input TEMP/base-images/images.tar\ndocker buildx build --build-context " + operatorImage + "=docker-image://base-image:0 --iidfile TEMP/block.iid -t block:c0ffee .", "id=sha256:feed\n"},
		{"images that cannot be loaded fail the step before any build", "load", false, "docker load --input TEMP/base-images/images.tar", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, log := stubbedDocker(t)
			temp, output := t.TempDir(), filepath.Join(t.TempDir(), "github-output")
			script, err := os.ReadFile("base-images.sh")
			if err != nil {
				t.Fatalf("reading the kit's base-images.sh: %v", err)
			}
			writeFile(t, filepath.Join(temp, "block-kit", "scripts", "base-images.sh"), string(script))
			writeFile(t, filepath.Join(temp, "base-images", "references"), operatorImage+" base-image:0\n")
			writeFile(t, filepath.Join(temp, "base-images", "images.tar"), "saved image\n")
			writeFile(t, output, "")

			out, exit := runNamed(t, "opentofu-conformance", "Build the block image", t.TempDir(), "", append(env, "STUB_FAIL="+tc.fail, "RUNNER_TEMP="+temp, "GITHUB_SHA=c0ffee", "GITHUB_OUTPUT="+output)...)

			if (exit == 0) != tc.wantExit {
				t.Fatalf("exit %d, output:\n%s\nwant success %v", exit, out, tc.wantExit)
			}
			if got := strings.ReplaceAll(dockerCalls(log), temp, "TEMP"); got != tc.wantDocker {
				t.Errorf("docker calls =\n%s\nwant\n%s", got, tc.wantDocker)
			}
			if got, _ := os.ReadFile(output); string(got) != tc.wantOutput {
				t.Errorf("$GITHUB_OUTPUT = %q, want %q", got, tc.wantOutput)
			}
		})
	}
}
