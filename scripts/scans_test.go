package scripts_test

// scan-secrets.sh, lint-scripts.sh and check-runtime-downloads.sh fail a throwaway repository holding what they guard against, pass it once that is gone, and run only a scanner whose sha256 matches its pin.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAWSKey is split so this file never holds the key a scan would report.
var fakeAWSKey = "AKIA" + "Q3EGRBCXZ5TVM2LN"

type change struct {
	write  map[string]string
	remove []string
}

func writes(path, content string) change { return change{write: map[string]string{path: content}} }

func removes(path string) change { return change{remove: []string{path}} }

// hermeticEnv keeps the caller's git config and storage variables out of the throwaway repository and the scans.
func hermeticEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=block-kit test", "GIT_AUTHOR_EMAIL=test@block-kit.invalid",
		"GIT_COMMITTER_NAME=block-kit test", "GIT_COMMITTER_EMAIL=test@block-kit.invalid")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = hermeticEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func gitRepo(t *testing.T, changes ...change) (dir string, shas []string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	for i, c := range changes {
		for path, content := range c.write {
			writeFile(t, filepath.Join(dir, path), content)
		}
		for _, path := range c.remove {
			git(t, dir, "rm", "-q", path)
		}
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "-m", "change "+string(rune('a'+i)))
		shas = append(shas, git(t, dir, "rev-parse", "HEAD"))
	}
	return dir, shas
}

func runBash(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", args...)
	cmd.Env = append(hermeticEnv(), env...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit):
		return string(out), exit.ExitCode()
	}
	t.Fatalf("running bash %s: %v\n%s", strings.Join(args, " "), err, out)
	return "", 0
}

func TestScanSecrets_FailsNamingTheCommitThatAddedAKey(t *testing.T) {
	repo, shas := gitRepo(t,
		writes("README.md", "# block\n"),
		writes("config.env", "aws_access_key_id = "+fakeAWSKey+"\n"),
		removes("config.env"))

	out, exit := runBash(t, nil, "scan-secrets.sh", repo)

	if exit != 1 {
		t.Errorf("scan-secrets.sh exit = %d; want 1\n%s", exit, out)
	}
	for _, want := range []string{shas[1], "aws-access-token", "config.env"} {
		if !strings.Contains(out, want) {
			t.Errorf("scan-secrets.sh output does not name %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, fakeAWSKey) {
		t.Errorf("scan-secrets.sh output carries the key unredacted:\n%s", out)
	}
}

func TestScanSecrets_PassesTheSameHistoryWithoutTheKey(t *testing.T) {
	repo, _ := gitRepo(t,
		writes("README.md", "# block\n"),
		writes("config.env", "aws_region = eu-central-1\n"),
		removes("config.env"))

	out, exit := runBash(t, nil, "scan-secrets.sh", repo)

	if exit != 0 {
		t.Errorf("scan-secrets.sh exit = %d; want 0\n%s", exit, out)
	}
}

func TestLintScripts(t *testing.T) {
	const unquoted, quoted = "#!/usr/bin/env bash\necho $1\n", "#!/usr/bin/env bash\necho \"$1\"\n"
	cases := []struct {
		name   string
		repo   []change
		exit   int
		naming []string
	}{
		{name: "a script with an unquoted variable fails with SC2086, naming it", repo: []change{writes("scripts/run.sh", unquoted)}, exit: 1,
			naming: []string{"SC2086", "scripts/run.sh"}},
		{name: "the same script with the variable quoted passes", repo: []change{writes("scripts/run.sh", quoted)}},
		{name: "a tracked script with a bash shebang and no extension is linted", repo: []change{writes("bin/deploy", unquoted)}, exit: 1,
			naming: []string{"SC2086", "bin/deploy"}},
		{name: "a repository with no shell script passes", repo: []change{writes("README.md", "# block\n")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, _ := gitRepo(t, c.repo...)

			out, exit := runBash(t, nil, "lint-scripts.sh", repo)

			if exit != c.exit {
				t.Errorf("lint-scripts.sh exit = %d; want %d\n%s", exit, c.exit, out)
			}
			for _, want := range c.naming {
				if !strings.Contains(out, want) {
					t.Errorf("lint-scripts.sh output does not name %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestPinnedTool(t *testing.T) {
	const member, good = "fake-1.0.0/fake", "#!/bin/sh\necho fake 1.0.0\n"
	goodSum, otherSum := sha256.Sum256([]byte(good)), sha256.Sum256([]byte("another binary"))
	src, release := t.TempDir(), filepath.Join(t.TempDir(), "fake.tar.gz")
	writeFile(t, filepath.Join(src, member), good)
	if out, err := exec.Command("tar", "-czf", release, "-C", src, member).CombinedOutput(); err != nil {
		t.Fatalf("building the release archive: %v\n%s", err, out)
	}
	cases := []struct {
		name, url, sha256, cached, refusal, wantCached string
	}{
		{name: "a release matching its pin is fetched into the cache", url: "file://" + release, sha256: hex.EncodeToString(goodSum[:]), wantCached: good},
		{name: "a release whose binary does not match its pin is refused and never cached", url: "file://" + release, sha256: hex.EncodeToString(otherSum[:]),
			refusal: "does not match sha256"},
		{name: "a corrupt cached binary is fetched again and replaced", url: "file://" + release, sha256: hex.EncodeToString(goodSum[:]), cached: "junk", wantCached: good},
		{name: "a cached binary matching its pin runs with no fetch", url: "file:///nowhere/fake.tar.gz", sha256: hex.EncodeToString(goodSum[:]), cached: good, wantCached: good},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cache := t.TempDir()
			bin := filepath.Join(cache, "block-kit", "fake-1.0.0", "fake")
			if c.cached != "" {
				writeFile(t, bin, c.cached)
			}

			out, exit := runBash(t, []string{"XDG_CACHE_HOME=" + cache},
				"-c", `. ./pinned-tool.sh && pinned_tool fake 1.0.0 "$1" "$2" "$3"`, "pinned-tool", c.url, member, c.sha256)

			want, wantExit := bin, 0
			if c.refusal != "" {
				want, wantExit = c.refusal, 1
			}
			if exit != wantExit || !strings.Contains(out, want) {
				t.Errorf("pinned_tool exit = %d, output %q; want %d and %q", exit, out, wantExit, want)
			}
			got, err := os.ReadFile(bin)
			if c.wantCached == "" && !errors.Is(err, os.ErrNotExist) || c.wantCached != "" && string(got) != c.wantCached {
				t.Errorf("cached binary = %q (err %v); want %q", got, err, c.wantCached)
			}
			if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(bin), "fetch.*")); len(leftovers) > 0 {
				t.Errorf("pinned_tool left %v in the cache", leftovers)
			}
		})
	}
}

func TestCheckRuntimeDownloads(t *testing.T) {
	const fetch = "curl -fsSL -o age.tar.gz https://github.com/FiloSottile/age/releases/download/v1.3.2/age-v1.3.2-linux-amd64.tar.gz\n"
	const copied = "bash copy-tool.sh ghcr.io/mitstack-eu/block-kit-tools:v1.0.0@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef /age/age " +
		"eb7dd1b518f0a307c99cd97782623c5321da049154b04acd2d98d21aa7bc9b2c /usr/local/bin/age\n"
	cases := []struct {
		name   string
		repo   []change
		exit   int
		naming []string
	}{
		{name: "a script that fetches its tool from a release page fails, naming the file and line", repo: []change{writes("scripts/host.sh", "#!/bin/sh\nset -eu\n"+fetch)},
			exit: 1, naming: []string{"scripts/host.sh:3:"}},
		{name: "Go source that renders a release-page URL fails, naming the file and line",
			repo: []change{writes("render.go", "package block\n\nconst k3s = \"https://github.com/k3s-io/k3s/releases/download/{{.K3sVersionPath}}/k3s\"\n")},
			exit: 1, naming: []string{"render.go:3:"}},
		{name: "a workflow the block commits into another repository fails", repo: []change{writes("templates/.github/workflows/restore.yml", "steps:\n  - run: "+fetch)},
			exit: 1, naming: []string{"templates/.github/workflows/restore.yml:2:"}},
		{name: "a script that copies its tool through copy-tool.sh passes", repo: []change{writes("scripts/host.sh", "#!/bin/sh\nset -eu\n"+copied)}},
		{name: "a release-page URL in test files, testdata and the repository's own workflows passes", repo: []change{
			writes("render_test.go", "package block\n\n// "+fetch), writes("scripts/host_test.sh", fetch), writes("scripts/test_host.py", "# "+fetch),
			writes("tests/main.tftest.hcl", "# "+fetch), writes("cluster/testdata/userdata.yaml", fetch), writes(".github/workflows/ci.yml", "steps:\n  - run: "+fetch)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, _ := gitRepo(t, c.repo...)

			out, exit := runBash(t, nil, "check-runtime-downloads.sh", repo)

			if exit != c.exit {
				t.Errorf("check-runtime-downloads.sh exit = %d; want %d\n%s", exit, c.exit, out)
			}
			for _, want := range c.naming {
				if !strings.Contains(out, want) {
					t.Errorf("check-runtime-downloads.sh output does not name %q:\n%s", want, out)
				}
			}
		})
	}
}
