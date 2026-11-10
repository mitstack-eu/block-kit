package main

// checkpolls fails on each sleep and polling-interval form a change adds, naming its file and line, and passes a change that adds none.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Each form is split so this file adds no line the check fails.
const (
	sleepCall  = "time." + "Sleep("
	tickCall   = "time." + "Tick("
	tickerCall = "time." + "NewTicker("
	interval   = "set" + "Interval("
	refetch    = "refetch" + "Interval"
	shSleep    = "sl" + "eep"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// repoAt commits files as the base of a new repository, enters it, and returns the base commit.
func repoAt(t *testing.T, files map[string]string) (dir, base string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir = t.TempDir()
	gitIn(t, dir, "init", "-q")
	write(t, dir, files)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	t.Chdir(dir)
	return dir, gitIn(t, dir, "rev-parse", "HEAD")
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "head")
}

func run(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = Run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestRun_FailsOnEachFormAChangeAddsNamingItsFileAndLine(t *testing.T) {
	for _, c := range []struct{ name, path, line, form string }{
		{"a time.Sleep call in the engine", "api/internal/lifecycle/wait.go", "\t" + sleepCall + "5 * time.Second)", "time.Sleep"},
		{"a time.Tick loop in an operator", "resource.go", "\tfor range " + tickCall + "time.Second) {", "time.Tick"},
		{"a time.NewTicker in the kit", "observe/wait.go", "\tt := " + tickerCall + "time.Minute)", "time.NewTicker"},
		{"a time.Sleep call in a scaffold template", "internal/scaffold/blockkitv1/resource.go.tmpl", "\t" + sleepCall + "time.Second)", "time.Sleep"},
		{"a setInterval call in the console", "app/src/hooks/usePoll.ts", "  const id = " + interval + "refresh, 2000)", "setInterval"},
		{"a refetchInterval option in the console", "app/src/pages/Part.tsx", "  useQuery({ " + refetch + ": 2000 })", "refetchInterval"},
		{"sleep and a number in a script", "scripts/wait.sh", shSleep + " 10", "a shell sleep"},
		{"sleep and a variable in a workflow run block", ".github/workflows/ci.yml", "          " + shSleep + ` "$DELAY"`, "a shell sleep"},
		{"sleep in a workflow template held in a Go string", "delivery_workflow.go", "          " + shSleep + " 15", "a shell sleep"},
		{"sleep after do in a shell loop", "scripts/until.sh", `until curl -sf "$url"; do ` + shSleep + " 5; done", "a shell sleep"},
		{"sleep on a one-line run", ".github/workflows/wait.yml", "      - run: " + shSleep + " 30", "a shell sleep"},
		{"sleep in a quoted OpenTofu command", "main.tf", `    command = "` + shSleep + ` 10"`, "a shell sleep"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, base := repoAt(t, map[string]string{c.path: "first\n"})
			write(t, dir, map[string]string{c.path: "first\n" + c.line + "\n"})
			commitAll(t, dir)
			stdout, stderr, code := run("-base", base)
			if want := c.path + ":2: adds " + c.form; code != 1 || !strings.Contains(stdout, want) {
				t.Errorf("exit %d, want 1 with %q\nstdout:\n%s\nstderr:\n%s", code, want, stdout, stderr)
			}
			if !strings.Contains(stdout, "the signal its upstream sends") {
				t.Errorf("the failure gives no advice:\n%s", stdout)
			}
		})
	}
}

func TestRun_FailsOnAnUntrackedFileThatPolls(t *testing.T) {
	dir, base := repoAt(t, nil)
	write(t, dir, map[string]string{"scripts/new.sh": "#!/bin/sh\n" + shSleep + " 5\n"})
	if stdout, _, code := run("-base", base); code != 1 || !strings.Contains(stdout, "scripts/new.sh:2: adds a shell sleep") {
		t.Errorf("exit %d, want 1 naming scripts/new.sh:2\n%s", code, stdout)
	}
}

func TestRun_PassesAChangeThatAddsNoPollLine(t *testing.T) {
	poll := "package x\n\n\t" + sleepCall + "time.Second)\nvar n = 1\n"
	prose := strings.Join([]string{sleepCall, tickCall, tickerCall, interval, refetch, shSleep + " 10"}, "\n")
	for _, c := range []struct {
		name       string
		base, head map[string]string
		rm         []string
		mv         [2]string
	}{
		{name: "deleting a poll line and a polling script", base: map[string]string{"wait.go": poll, "wait.sh": shSleep + " 10\n"},
			head: map[string]string{"wait.go": "package x\n\nvar n = 1\n"}, rm: []string{"wait.sh"}},
		{name: "changing the line beside an old poll", base: map[string]string{"wait.go": poll},
			head: map[string]string{"wait.go": strings.Replace(poll, "n = 1", "n = 2", 1)}},
		{name: "moving a file that holds a poll", base: map[string]string{"wait.go": poll}, mv: [2]string{"wait.go", "moved.go"}},
		{name: "naming every form in prose", head: map[string]string{"docs/waits.md": prose, "NOTES.txt": prose}},
		{name: "a sleep in a comment", head: map[string]string{"wait.sh": "# retry; " + shSleep + " 5\n", "wait.go": "package x\n\n// then " + sleepCall + "time.Second)\n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, base := repoAt(t, c.base)
			write(t, dir, c.head)
			for _, p := range c.rm {
				gitIn(t, dir, "rm", "-q", p)
			}
			if c.mv[0] != "" {
				gitIn(t, dir, "mv", c.mv[0], c.mv[1])
			}
			commitAll(t, dir)
			if stdout, stderr, code := run("-base", base); code != 0 || !strings.Contains(stdout, "checkpolls: judged") {
				t.Errorf("exit %d, want 0 with the judged count\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
		})
	}
}

func TestRun_TakesOnlyTheBaseRevision(t *testing.T) {
	_, base := repoAt(t, map[string]string{"a.go": "package a\n"})
	_, help, code := run("-help")
	flags := 0
	for _, line := range strings.Split(help, "\n") {
		if strings.HasPrefix(line, "  -") {
			flags++
		}
	}
	if code != 0 || flags != 1 || !strings.Contains(help, "  -base") {
		t.Errorf("-help exit %d with %d flags, want 0 with -base alone:\n%s", code, flags, help)
	}
	for name, args := range map[string][]string{
		"no base":         nil,
		"a path to skip":  {"-base", base, "api"},
		"a skip flag":     {"-skip", "api", "-base", base},
		"an unknown base": {"-base", "no-such-commit"},
	} {
		if _, stderr, code := run(args...); code != 2 {
			t.Errorf("%s: exit %d, want 2\n%s", name, code, stderr)
		}
	}
}
