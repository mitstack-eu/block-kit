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
