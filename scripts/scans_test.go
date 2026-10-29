package scripts_test

// scan-secrets.sh and lint-scripts.sh fail a throwaway repository holding what they guard against, and pass it once that is gone.

import (
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

// gitRepo commits each change in turn to a new repository and returns it with each commit's sha.
func gitRepo(t *testing.T, changes ...change) (dir string, shas []string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	for i, c := range changes {
		for path, content := range c.write {
			full := filepath.Join(dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
				t.Fatal(err)
			}
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

// runScan returns the script's combined output and exit code.
func runScan(t *testing.T, script, repo string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", script, repo)
	cmd.Env = hermeticEnv()
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit):
		return string(out), exit.ExitCode()
	}
	t.Fatalf("running %s on %s: %v\n%s", script, repo, err, out)
	return "", 0
}

func TestScanSecrets_FailsNamingTheCommitThatAddedAKey(t *testing.T) {
	repo, shas := gitRepo(t,
		writes("README.md", "# block\n"),
		writes("config.env", "aws_access_key_id = "+fakeAWSKey+"\n"),
		removes("config.env"))

	out, exit := runScan(t, "scan-secrets.sh", repo)

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

	out, exit := runScan(t, "scan-secrets.sh", repo)

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

			out, exit := runScan(t, "lint-scripts.sh", repo)

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
