package scripts_test

// base-images.sh pull reads the Dockerfile through GitHub's API, pulls and saves every image it names in the registry, and lists them for the build.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	verifyRegistry = "123456789012.dkr.ecr.eu-central-1.amazonaws.com"
	headCommit     = "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff"
)

var operatorImage = verifyRegistry + "/mitstack-opentofu-operator@sha256:" + strings.Repeat("ab", 32)

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
			if got := dockerCalls(t, log); got != strings.Join(wantDocker, "\n") {
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
