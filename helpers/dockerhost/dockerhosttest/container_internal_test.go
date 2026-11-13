package dockerhosttest

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
)

// A rendered service is created as the host runs it: its limits, its own data volume, its labels, its health check,
// its own environment beside the given one, its network, and its entrypoint with Compose's "$$" read back as "$".
func TestCreateArgs_RunTheServiceAsTheHostWouldOnItsOwnVolumeWithItsLabelsAndEnvironmentBesideTheOnesGiven(t *testing.T) {
	s := dockerhost.ComposeService{
		Image: "example.org/store:2.1@sha256:0f9e", CPUs: "0.5", MemLimit: 536870912, MemswapLimit: 536870912,
		Entrypoint:  []string{"sh", "-c", `exec store --password="$$DB_PASSWORD"`, "store"},
		Command:     []string{"serve"},
		Volumes:     []string{"store-data:/data"},
		SecurityOpt: []string{"no-new-privileges:true"},
		Healthcheck: dockerhost.ComposeHealth{Test: []string{"CMD", "store", "ping"}},
		Labels:      map[string]string{"mitstack.dev/subscription": "sub_db_shop", "mitstack.dev/app": "shop"},
		Environment: map[string]string{"STORE_MODE": "strict", "STORE_HOME": "/data/$$HOME"},
	}

	got := createArgs("scratch-db", s, "scratch-net", []string{"DB_PASSWORD=pw$1"})

	want := []string{
		"create", "--name", "scratch-db", "--cpus", "0.5", "--memory", "536870912", "--memory-swap", "536870912",
		"--volume", "scratch-db:/data", "--security-opt", "no-new-privileges:true",
		"--label", "mitstack.dev/app=shop", "--label", "mitstack.dev/subscription=sub_db_shop",
		"--health-cmd", "store ping", "--health-interval", "1s",
		"--env", "STORE_HOME=/data/$HOME", "--env", "STORE_MODE=strict", "--env", "DB_PASSWORD=pw$1", "--network", "scratch-net",
		"--entrypoint", "sh", "example.org/store:2.1@sha256:0f9e", "-c", `exec store --password="$DB_PASSWORD"`, "store", "serve",
	}
	if !slices.Equal(got, want) {
		t.Errorf("createArgs =\n%q\nwant\n%q", got, want)
	}
	if got := createArgs("scratch-db", s, "", nil); slices.Contains(got, "--network") {
		t.Errorf("createArgs with no network = %q; want the default network", got)
	}
}

// stubDocker puts first on PATH a docker that runs script, so a test reads a stream the waits follow without a daemon.
func stubDocker(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("writing the stub docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestFollow_AMatchingLineOnEitherStreamEndsTheCommand(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	stubDocker(t, "echo $$ > "+pidFile+"\necho noise\necho 'the marker line' >&2\nexec tail -f /dev/null\n")

	found, err := follow(func(line string) bool { return strings.Contains(line, "marker") }, "logs", "--follow", "db")

	if !found || err != nil {
		t.Fatalf("follow = %v, %v; want the marker found on the command's error output", found, err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("reading the stub's pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("the stub's pid reads %q: %v", raw, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("signalling the stream's command %d answered %v; want it ended and reaped", pid, err)
	}
}

func TestFollow_AnOutputThatEndsBeforeAMatchSaysSo(t *testing.T) {
	stubDocker(t, "echo noise\n")

	found, err := follow(func(string) bool { return false }, "logs", "--follow", "db")

	if found || err == nil || !strings.Contains(err.Error(), "the output ended before a line matched") {
		t.Errorf("follow = %v, %v; want no match and an error saying the output ended", found, err)
	}
}

func TestFollow_ReadsALineLongerThanTheScannersDefaultBuffer(t *testing.T) {
	stubDocker(t, "head -c 70000 /dev/zero | tr '\\0' x\necho\necho marker\n")

	found, err := follow(func(line string) bool { return line == "marker" }, "logs", "--follow", "db")

	if !found || err != nil {
		t.Errorf("follow past a 70000-byte line = %v, %v; want the marker after it found", found, err)
	}
}
