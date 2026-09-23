package main

// main_test.go pins the CONTRACT for the `pr-keys` command: the CLI a
// pull_request CI job calls to gate the pull request on whether its range
// delivers a board key. The seam under test is
// Run(args []string, stdout io.Writer) int, following cmd/board-delivery's
// own idiom: the whole command is testable without a real process.
//
// The git half runs against a hermetic temporary repository built with
// os/exec (git init/commit), mirroring cmd/board-delivery/main_test.go's
// fixture minus the anchor-tag and bare-origin machinery this command has no
// use for: pr-keys only ever reads a base..head range, and never writes to
// the repository.

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

type testRepo struct {
	Dir string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	r := &testRepo{Dir: dir}
	r.commit(t, "seed", "")
	return r
}

// commit makes an empty commit (subject, optional body) and returns its sha.
func (r *testRepo) commit(t *testing.T, subject, body string) string {
	t.Helper()
	args := []string{"-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "--allow-empty", "-m", subject}
	if body != "" {
		args = append(args, "-m", body)
	}
	runGit(t, r.Dir, args...)
	return runGit(t, r.Dir, "rev-parse", "HEAD")
}

// runPRKeys invokes Run with --repo/--base/--head/--title exactly as the
// workflow step will set them.
func runPRKeys(t *testing.T, repoDir, base, head, title string) (stdout string, exitCode int) {
	t.Helper()
	args := []string{"--repo", repoDir, "--base", base, "--head", head, "--title", title}
	var buf bytes.Buffer
	exitCode = Run(args, &buf)
	return buf.String(), exitCode
}

func TestRun_KeyedCommitAndKeyedTitle_ExitsZero(t *testing.T) {
	repo := newTestRepo(t)
	base := runGit(t, repo.Dir, "rev-parse", "HEAD")
	head := repo.commit(t, "MIT-12: fix the thing", "")

	out, code := runPRKeys(t, repo.Dir, base, head, "MIT-12: fix the thing")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0, output: %s", code, out)
	}
}

// TestRun_KitPR6Shape_ExitsNonZeroAndNamesCommits exercises the range
// reading through real git: a merge commit whose branch commits carry no
// key, a merge subject naming the key only inside the branch name, and a
// keyed title, which must fail on the commits.
func TestRun_KitPR6Shape_ExitsNonZeroAndNamesCommits(t *testing.T) {
	repo := newTestRepo(t)
	base := runGit(t, repo.Dir, "rev-parse", "HEAD")
	repo.commit(t, "delete gets instance config", "")
	repo.commit(t, "fix review comments", "")
	head := repo.commit(t, "Merge pull request #6 from mitstack-eu/fix/MIT-3149-delete-gets-instance-config", "")

	out, code := runPRKeys(t, repo.Dir, base, head, "MIT-3149: delete gets instance config")
	if code == 0 {
		t.Fatalf("Run exit code = 0, want non-zero — kit PR 6's branch commits carry no key")
	}
	if !strings.Contains(strings.ToLower(out), "commit") {
		t.Errorf("output = %q, want it to name the commits as missing", out)
	}
}

func TestRun_KeylessTitle_ExitsNonZeroAndNamesTitle(t *testing.T) {
	repo := newTestRepo(t)
	base := runGit(t, repo.Dir, "rev-parse", "HEAD")
	head := repo.commit(t, "MIT-12: fix the thing", "")

	out, code := runPRKeys(t, repo.Dir, base, head, "fix the thing")
	if code == 0 {
		t.Fatalf("Run exit code = 0, want non-zero — the title carries no key")
	}
	if !strings.Contains(strings.ToLower(out), "title") {
		t.Errorf("output = %q, want it to name the title as missing", out)
	}
}

// TestRun_EmptyRange_ExitsNonZero pins that an empty base..head range never
// passes vacuously on a keyed title alone.
func TestRun_EmptyRange_ExitsNonZero(t *testing.T) {
	repo := newTestRepo(t)
	head := runGit(t, repo.Dir, "rev-parse", "HEAD")

	out, code := runPRKeys(t, repo.Dir, head, head, "MIT-12: fix the thing")
	if code == 0 {
		t.Fatalf("Run exit code = 0, want non-zero — an empty range must never pass")
	}
	if !strings.Contains(strings.ToLower(out), "commit") {
		t.Errorf("output = %q, want it to name the commits as missing", out)
	}
}

func TestRun_MissingHeadFlag_ExitsTwo(t *testing.T) {
	var buf bytes.Buffer
	code := Run([]string{"--repo", ".", "--base", "HEAD", "--title", "MIT-1: x"}, &buf)
	if code != 2 {
		t.Fatalf("Run exit code = %d, want 2 — --head is required, output: %s", code, buf.String())
	}
}

func TestRun_MissingBaseFlag_ExitsTwo(t *testing.T) {
	var buf bytes.Buffer
	code := Run([]string{"--repo", ".", "--head", "HEAD", "--title", "MIT-1: x"}, &buf)
	if code != 2 {
		t.Fatalf("Run exit code = %d, want 2 — --base is required, output: %s", code, buf.String())
	}
}

// TestRun_TitleFallsBackToEnvWhenFlagOmitted pins that the title can arrive
// through PR_TITLE, not only through --title, since the workflow step reads
// it from the pull_request event either way.
func TestRun_TitleFallsBackToEnvWhenFlagOmitted(t *testing.T) {
	repo := newTestRepo(t)
	base := runGit(t, repo.Dir, "rev-parse", "HEAD")
	head := repo.commit(t, "MIT-12: fix the thing", "")

	t.Setenv("PR_TITLE", "MIT-12: fix the thing")
	var buf bytes.Buffer
	code := Run([]string{"--repo", repo.Dir, "--base", base, "--head", head}, &buf)
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0 — the title should fall back to PR_TITLE, output: %s", code, buf.String())
	}
}
