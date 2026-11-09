package tools_test

// copy-tool.sh copies one file out of an image named by digest, through Docker where it runs and otherwise over the registry's HTTP API with an anonymous token, reading only the layer that holds the file, and refuses a manifest, layer or file whose digest differs.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/tools"
	"github.com/mitstack-eu/block-kit/helpers/tools/toolstest"
)

const (
	repo, ageBody, k3sBody = "mitstack-eu/block-kit-tools", "age binary", "k3s installer"
	fakeDocker             = `#!/usr/bin/env bash
case "$1" in
  info | create | rm) echo "$*" >> "$STUB_LOG"; [ "$1" != create ] || echo c0ffee ;;
  cp) echo "cp $2" >> "$STUB_LOG"; cp "$STUB_ROOT/${2#*:}" "$3" ;;
esac
`
)

func sha(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func pushed(t *testing.T) (*toolstest.Registry, toolstest.Image) {
	t.Helper()
	r := toolstest.New(t)
	return r, r.Push(t, repo, toolstest.File{Path: "/age/age", Body: ageBody}, toolstest.File{Path: "/k3s/install.sh", Body: k3sBody})
}

// runScript runs the script CopyScript holds with env added to the test's own.
func runScript(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "copy-tool.sh")
	writeFile(t, script, tools.CopyScript)
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	raw, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("running copy-tool.sh: %v", err)
	}
	return string(raw), cmd.ProcessState.ExitCode()
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil || os.WriteFile(name, []byte(content), 0o755) != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func wantCopy(t *testing.T, out string, exit, wantExit int, dest, wantBody string, naming ...string) {
	t.Helper()
	got, err := os.ReadFile(dest)
	if exit != wantExit || wantBody == "" && !errors.Is(err, os.ErrNotExist) || wantBody != "" && string(got) != wantBody {
		t.Errorf("copy-tool.sh exit = %d, destination %q (%v); want %d and %q\n%s", exit, got, err, wantExit, wantBody, out)
	}
	for _, want := range naming {
		if !strings.Contains(out, want) {
			t.Errorf("copy-tool.sh output does not name %q:\n%s", want, out)
		}
	}
}

func TestCopyTool_CopiesAFileOverTheRegistryAPIReadingOnlyTheLayerThatHoldsIt(t *testing.T) {
	for i, tc := range []struct{ path, body string }{{"/age/age", ageBody}, {"/k3s/install.sh", k3sBody}} {
		t.Run(tc.path, func(t *testing.T) {
			r, img := pushed(t)
			dest := filepath.Join(t.TempDir(), "bin", "tool")

			out, exit := runScript(t, []string{r.Env()}, img.Ref, tc.path, sha(tc.body), dest)

			wantCopy(t, out, exit, 0, dest, tc.body)
			if want := []string{img.Index, img.Manifest, img.Config, img.Layers[i]}; !slices.Equal(r.Reads(), want) {
				t.Errorf("registry reads = %q; want the index, the amd64 manifest, its config and the one layer %q", r.Reads(), want)
			}
		})
	}
}

func TestCopyTool_RefusesWhatDiffersFromTheDigestThatNamesIt(t *testing.T) {
	const other = "other bytes"
	cases := []struct {
		name string
		read func(toolstest.Image) string
	}{
		{"an index", func(img toolstest.Image) string { return "manifest " + img.Index }},
		{"the amd64 manifest", func(img toolstest.Image) string { return "manifest " + img.Manifest }},
		{"the image config", func(img toolstest.Image) string { return "config " + img.Config }},
		{"age's layer", func(img toolstest.Image) string { return "layer " + img.Layers[0] }},
		{"a file whose sha256 differs from its pin", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, img := pushed(t)
			pin, naming := sha(ageBody), []string{img.Ref, "file /age/age", sha(ageBody), sha(other)}
			if tc.read != nil {
				read := tc.read(img)
				r.Swap(strings.Fields(read)[1], []byte(other))
				naming = []string{img.Ref, read, toolstest.Digest([]byte(other))}
			} else {
				pin = sha(other)
			}
			dest := filepath.Join(t.TempDir(), "age")

			out, exit := runScript(t, []string{r.Env()}, img.Ref, "/age/age", pin, dest)

			wantCopy(t, out, exit, 1, dest, "", naming...)
		})
	}
}

func TestCopyTool_NamesTheImageAndTheReadThatFailed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault func(*toolstest.Registry, toolstest.Image) string
	}{
		{"a layer the registry cannot serve", func(r *toolstest.Registry, img toolstest.Image) string {
			r.Fail(img.Layers[0], http.StatusNotFound)
			return "layer " + img.Layers[0]
		}},
		{"a package that gives no anonymous token", func(r *toolstest.Registry, _ toolstest.Image) string {
			r.RefuseTokens(http.StatusUnauthorized)
			return "the pull token"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, img := pushed(t)
			read, dest := tc.fault(r, img), filepath.Join(t.TempDir(), "age")

			out, exit := runScript(t, []string{r.Env()}, img.Ref, "/age/age", sha(ageBody), dest)

			wantCopy(t, out, exit, 1, dest, "", img.Ref, read)
		})
	}
}

func TestCopyTool_CopiesThroughDockerWhereDockerRuns(t *testing.T) {
	for _, tc := range []struct {
		name, pin, body string
		exit            int
		naming          []string
	}{
		{name: "a file matching its pin is copied", pin: sha(ageBody), body: ageBody},
		{name: "a file whose sha256 differs from its pin is refused, naming both sums", pin: sha("other"), exit: 1, naming: []string{sha(ageBody), sha("other")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, ref := t.TempDir(), "registry.invalid/"+repo+":v1@sha256:"+sha("index")
			writeFile(t, filepath.Join(dir, "bin", "docker"), fakeDocker)
			writeFile(t, filepath.Join(dir, "root", "age", "age"), ageBody)
			log, dest := filepath.Join(dir, "docker.log"), filepath.Join(dir, "age")

			out, exit := runScript(t, []string{"PATH=" + filepath.Join(dir, "bin") + ":" + os.Getenv("PATH"), "STUB_LOG=" + log, "STUB_ROOT=" + filepath.Join(dir, "root")},
				ref, "/age/age", tc.pin, dest)

			wantCopy(t, out, exit, tc.exit, dest, tc.body, append(tc.naming, ref)...)
			if calls, _ := os.ReadFile(log); string(calls) != "info\ncreate --platform linux/amd64 "+ref+" /copy-tool\ncp c0ffee:/age/age\nrm c0ffee\n" {
				t.Errorf("docker calls = %q; want info, create, cp of /age/age and rm", calls)
			}
		})
	}
}

func TestCopyTool_RefusesAnImageNotNamedByDigest(t *testing.T) {
	r, img := pushed(t)

	out, exit := runScript(t, []string{r.Env()}, strings.TrimSuffix(img.Ref, "@"+img.Index), "/age/age", sha(ageBody), filepath.Join(t.TempDir(), "age"))

	if exit != 2 || !strings.Contains(out, "Usage") {
		t.Errorf("copy-tool.sh with no digest exit = %d; want 2 and its usage\n%s", exit, out)
	}
}
