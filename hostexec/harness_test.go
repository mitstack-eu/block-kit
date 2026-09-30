package hostexec_test

// Shared fixtures for the hostexec suite. Both drivers are proven against a
// PATH shim rather than a real aws/ssh binary, following the kit's established
// idiom (see cloudid_test.go's fakeAWS): a stub executable records what it was
// called with, so a test can assert on argv and stdin without ever touching
// the network. Recording stdin separately from argv is what lets a test prove
// a value arrived over stdin, rather than only that it is absent from argv.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/hostexec"
)

func setPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fakeSSM writes a stub `aws` executable that understands only the two ssm
// subcommands the ssm driver issues. send-command always echoes
// sendCommandID. get-command-invocation fails pollFailures times first (each
// failure proving a transient poll error must be retried, not treated as a
// terminal status), then serves statuses in order, holding the last entry for
// any call beyond the end of the slice.
func fakeSSM(t *testing.T, sendCommandID string, statuses []string, pollFailures int) (dir, argvRecord, stdinRecord string) {
	t.Helper()
	dir = t.TempDir()
	argvRecord = filepath.Join(dir, "argv")
	stdinRecord = filepath.Join(dir, "stdin")
	countFile := filepath.Join(dir, "count")

	quoted := make([]string, len(statuses))
	for i, s := range statuses {
		quoted[i] = shQuote(s)
	}

	script := fmt.Sprintf(`#!/usr/bin/env bash
set -u
for a in "$@"; do printf '%%s\n' "$a" >> %[1]s; done
printf -- '---\n' >> %[1]s
cat >> %[2]s
printf -- '---\n' >> %[2]s

if [ "${2:-}" = "send-command" ]; then
  printf '%%s\n' %[3]s
  exit 0
fi

if [ "${2:-}" = "get-command-invocation" ]; then
  n=0
  [ -f %[4]s ] && n=$(cat %[4]s)
  echo $((n+1)) > %[4]s
  if [ "$n" -lt %[5]d ]; then
    echo "fake ssm: transient poll failure" >&2
    exit 1
  fi
  idx=$((n - %[5]d))
  STATUSES=(%[6]s)
  last=$((${#STATUSES[@]} - 1))
  if [ "$idx" -gt "$last" ]; then idx=$last; fi
  printf '%%s\n' "${STATUSES[$idx]}"
  exit 0
fi

echo "fake aws: unrecognised subcommand: $*" >&2
exit 2
`, argvRecord, stdinRecord, shQuote(sendCommandID), countFile, pollFailures, strings.Join(quoted, " "))

	bin := filepath.Join(dir, "aws")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake aws: %v", err)
	}
	return dir, argvRecord, stdinRecord
}

// ssmHost returns a Host that dispatches to the ssm driver with a target and
// region the argv assertions below are pinned to.
func ssmHost() hostexec.Host {
	return hostexec.Host{Kind: hostexec.KindSSM, Target: "i-0123456789abcdef0", Region: "eu-central-1"}
}

// fakeSSH writes a stub `ssh` executable running body after recording: every
// argv element (one per line, invocations separated by "---"), the full
// stdin it received, and — whenever a "-i" flag is present — the permission
// bits and path of the file it points at, captured while the file still
// exists so a test can assert on both before Run cleans it up.
func fakeSSH(t *testing.T, body string) (dir, argvRecord, stdinRecord, permRecord, idPathRecord string) {
	t.Helper()
	dir = t.TempDir()
	argvRecord = filepath.Join(dir, "argv")
	stdinRecord = filepath.Join(dir, "stdin")
	permRecord = filepath.Join(dir, "perm")
	idPathRecord = filepath.Join(dir, "idpath")

	script := fmt.Sprintf(`#!/usr/bin/env bash
set -u
for a in "$@"; do printf '%%s\n' "$a" >> %[1]s; done
printf -- '---\n' >> %[1]s
cat >> %[2]s
printf -- '---\n' >> %[2]s
prev=""
for a in "$@"; do
  if [ "$prev" = "-i" ]; then
    stat -c '%%a' "$a" > %[3]s 2>/dev/null
    printf '%%s' "$a" > %[4]s
  fi
  prev="$a"
done
%[5]s
`, argvRecord, stdinRecord, permRecord, idPathRecord, body)

	bin := filepath.Join(dir, "ssh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	return dir, argvRecord, stdinRecord, permRecord, idPathRecord
}

// sshHost returns a Host that dispatches to the ssh driver, carrying
// credential as the PEM the driver must write to a private temp file.
func sshHost(credential string) hostexec.Host {
	return hostexec.Host{Kind: hostexec.KindSSH, Target: "deploy@10.0.0.5", Credential: credential}
}

// fakeSSMOutput is fakeSSM's stub `aws` plus a second get-command-invocation
// response: RunOutput issues that call twice, once with --query Status (the
// existing poll loop) and once with --query StandardOutputContent, and
// fakeSSM's argv-position dispatch ("${2:-}" alone) cannot tell those two
// apart. This fake inspects the --query VALUE instead: a StandardOutputContent
// query always serves stdout, unconditionally and without consuming a poll
// slot, while a Status query keeps fakeSSM's own pollFailures / statuses
// behaviour untouched.
func fakeSSMOutput(t *testing.T, sendCommandID string, statuses []string, pollFailures int, stdout string) (dir, argvRecord, stdinRecord string) {
	t.Helper()
	dir = t.TempDir()
	argvRecord = filepath.Join(dir, "argv")
	stdinRecord = filepath.Join(dir, "stdin")
	countFile := filepath.Join(dir, "count")

	quoted := make([]string, len(statuses))
	for i, s := range statuses {
		quoted[i] = shQuote(s)
	}

	script := fmt.Sprintf(`#!/usr/bin/env bash
set -u
for a in "$@"; do printf '%%s\n' "$a" >> %[1]s; done
printf -- '---\n' >> %[1]s
cat >> %[2]s
printf -- '---\n' >> %[2]s

if [ "${2:-}" = "send-command" ]; then
  printf '%%s\n' %[3]s
  exit 0
fi

if [ "${2:-}" = "get-command-invocation" ]; then
  query=""
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--query" ]; then query="$a"; fi
    prev="$a"
  done
  if [ "$query" = "StandardOutputContent" ]; then
    printf '%%s\n' %[7]s
    exit 0
  fi
  n=0
  [ -f %[4]s ] && n=$(cat %[4]s)
  echo $((n+1)) > %[4]s
  if [ "$n" -lt %[5]d ]; then
    echo "fake ssm: transient poll failure" >&2
    exit 1
  fi
  idx=$((n - %[5]d))
  STATUSES=(%[6]s)
  last=$((${#STATUSES[@]} - 1))
  if [ "$idx" -gt "$last" ]; then idx=$last; fi
  printf '%%s\n' "${STATUSES[$idx]}"
  exit 0
fi

echo "fake aws: unrecognised subcommand: $*" >&2
exit 2
`, argvRecord, stdinRecord, shQuote(sendCommandID), countFile, pollFailures, strings.Join(quoted, " "), shQuote(stdout))

	bin := filepath.Join(dir, "aws")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake aws: %v", err)
	}
	return dir, argvRecord, stdinRecord
}

// ssmQueryOrder returns the --query value of every get-command-invocation
// call recorded in argvRecord, in call order, so a test can assert the
// StandardOutputContent read genuinely happens only after every earlier call
// was a Status poll — not merely that both queries happened at some point.
func ssmQueryOrder(t *testing.T, argvRecord string) []string {
	t.Helper()
	raw, err := os.ReadFile(argvRecord)
	if err != nil {
		t.Fatalf("reading fake aws argv record: %v", err)
	}
	var queries []string
	for _, inv := range strings.Split(string(raw), "---\n") {
		lines := strings.Split(strings.TrimRight(inv, "\n"), "\n")
		if len(lines) < 2 || lines[0] != "ssm" || lines[1] != "get-command-invocation" {
			continue
		}
		for i, l := range lines {
			if l == "--query" && i+1 < len(lines) {
				queries = append(queries, lines[i+1])
				break
			}
		}
	}
	return queries
}

// fakeSSMHeld writes a stub `aws` whose get-command-invocation Status answer
// stays InProgress for as long as holdFile exists, the way a command still
// running on the box does, and answers finalStatus once the test removes it.
// Every get-command-invocation call first creates pollStarted, so a test can
// wait until the driver has sent the command and begun polling. A
// StandardOutputContent query always serves stdout. holdFile is created
// armed; removing it releases the command. hangStatusPoll makes every Status
// query hang instead.
func fakeSSMHeld(t *testing.T, sendCommandID, finalStatus, stdout string) (dir, argvRecord, pollStarted, holdFile string) {
	t.Helper()
	dir = t.TempDir()
	argvRecord = filepath.Join(dir, "argv")
	pollStarted = filepath.Join(dir, "poll-started")
	holdFile = filepath.Join(dir, "hold-in-progress")

	script := fmt.Sprintf(`#!/usr/bin/env bash
set -u
for a in "$@"; do printf '%%s\n' "$a" >> %[1]s; done
printf -- '---\n' >> %[1]s

if [ "${2:-}" = "send-command" ]; then
  printf '%%s\n' %[2]s
  exit 0
fi

if [ "${2:-}" = "get-command-invocation" ]; then
  : > %[3]s
  query=""
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--query" ]; then query="$a"; fi
    prev="$a"
  done
  if [ "$query" = "StandardOutputContent" ]; then
    printf '%%s\n' %[6]s
    exit 0
  fi
  if [ -f %[7]s ]; then
    exec sleep %[8]d
  fi
  if [ -f %[4]s ]; then
    printf '%%s\n' InProgress
    exit 0
  fi
  printf '%%s\n' %[5]s
  exit 0
fi

echo "fake aws: unrecognised subcommand: $*" >&2
exit 2
`, argvRecord, shQuote(sendCommandID), pollStarted, holdFile, shQuote(finalStatus), shQuote(stdout),
		filepath.Join(dir, hangStatusMarker), int(hungStatusSleep/time.Second))

	bin := filepath.Join(dir, "aws")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake aws: %v", err)
	}
	if err := os.WriteFile(holdFile, nil, 0o644); err != nil {
		t.Fatalf("arming the in-progress hold: %v", err)
	}
	return dir, argvRecord, pollStarted, holdFile
}

// hangStatusMarker names the file in a fakeSSMHeld dir that makes every
// Status query hang; hungStatusSleep bounds that hang. The stub execs sleep,
// so killing the Status call kills the sleep itself and leaves no process
// holding its pipes, and an unkilled call still ends after hungStatusSleep.
const (
	hangStatusMarker = "hang-status-poll"
	hungStatusSleep  = 10 * time.Second
)

// hangStatusPoll makes the fakeSSMHeld stub in dir hang on every Status query
// for hungStatusSleep instead of answering it.
func hangStatusPoll(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, hangStatusMarker), nil, 0o644); err != nil {
		t.Fatalf("arming the Status call hang: %v", err)
	}
}

// setSSMPolling sets SSMPollInterval and SSMPollTimeout for one test and
// restores both on cleanup. A test that sets them must not run in parallel
// with another test that reads them.
func setSSMPolling(t *testing.T, interval, timeout time.Duration) {
	t.Helper()
	origInterval, origTimeout := hostexec.SSMPollInterval, hostexec.SSMPollTimeout
	hostexec.SSMPollInterval = interval
	hostexec.SSMPollTimeout = timeout
	t.Cleanup(func() {
		hostexec.SSMPollInterval = origInterval
		hostexec.SSMPollTimeout = origTimeout
	})
}

// runInBackground runs call on its own goroutine and delivers its error on
// the returned channel. On cleanup it removes holdFile, releasing a held
// fakeSSMHeld command, and waits for call to return, so a failed test never
// leaves a call running that still reads the package variables
// setSSMPolling restores. Call setSSMPolling and fakeSSMHeld before this so
// that wait runs before either of their cleanups.
func runInBackground(t *testing.T, holdFile string, call func() error) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- call()
	}()
	t.Cleanup(func() {
		_ = os.Remove(holdFile)
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Error("the call under test was still running 30s after its hold was released")
		}
	})
	return done
}

// waitForFile waits until path exists, failing early if done delivers first,
// since the call under test then returned before reaching the point path
// marks.
func waitForFile(t *testing.T, path string, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("the call returned (err=%v) before %s was created", err, filepath.Base(path))
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// sendCommandArgv returns the argv of the first send-command call recorded
// in argvRecord, one element per line as fakeSSM, fakeSSMOutput and
// fakeSSMHeld record it. It fails the test when no send-command was
// recorded.
func sendCommandArgv(t *testing.T, argvRecord string) []string {
	t.Helper()
	raw, err := os.ReadFile(argvRecord)
	if err != nil {
		t.Fatalf("reading fake aws argv record: %v", err)
	}
	for _, inv := range strings.Split(string(raw), "---\n") {
		lines := strings.Split(strings.TrimRight(inv, "\n"), "\n")
		if len(lines) >= 2 && lines[0] == "ssm" && lines[1] == "send-command" {
			return lines
		}
	}
	t.Fatal("fake aws never recorded a send-command call")
	return nil
}
