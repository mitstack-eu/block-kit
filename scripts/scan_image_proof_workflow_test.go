package scripts_test

// scan-image-proof.yml's scan step passes only when scan-image.sh exits as the matrix expects, accepts each finding the row lists as accepted in the provider and refuses it nowhere, refuses each one it lists as refused there and accepts it nowhere, and records the scan in the step summary.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/verify/kittest"
)

const (
	proofBinary   = "var/cache/tofu-plugins/registry.opentofu.org/scaleway/scaleway/2.92.1/linux_amd64/terraform-provider-scaleway_v2.92.1"
	accepted      = "accepted GO-2026-5932 in " + proofBinary + ": its Go function table names 324"
	acceptedXNet  = "accepted CVE-2026-78661 in " + proofBinary + ": the binary's sha256 is the release's"
	refusedStdlib = "refused CVE-2026-97054 in " + proofBinary + ": stdlib v1.27.3 has a fix in 1.26.11, 1.27.4"
)

func TestScanImageProof_PassesOnlyWhenTheScanExitsAcceptsAndRefusesAsTheRowLists(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "scan-image-proof.yml"))
	if err != nil {
		t.Fatal(err)
	}
	step, err := kittest.ReadWorkflowScript(workflow, "real-provider", "Scan the image, expecting exit ${{ matrix.exit }} and each listed finding accepted or refused")
	if err != nil {
		t.Fatal(err)
	}
	const listAccepted, listRefused = "GO-2026-5932 CVE-2026-78661", "CVE-2026-97054"
	all := strings.Join([]string{accepted, acceptedXNet, refusedStdlib}, "\n")
	cases := []struct {
		name             string
		scanExit, want   int
		refused, scanOut string
		pass             bool
	}{
		{"a scan exiting 1 as expected, accepting and refusing as the row lists, passes", 1, 1, listRefused, all, true},
		{"a scan exiting 0 as expected, with nothing listed as refused, passes", 0, 0, "", accepted + "\n" + acceptedXNet, true},
		{"a scan exiting 1 where 0 is expected fails", 1, 0, listRefused, all, false},
		{"a scan exiting 0 where 1 is expected fails", 0, 1, listRefused, all, false},
		{"a scan that never accepts a finding listed as accepted fails", 1, 1, listRefused, accepted + "\n" + refusedStdlib, false},
		{"a scan that refuses a finding listed as accepted anywhere fails", 1, 1, listRefused, all + "\nrefused GO-2026-5932 in usr/local/bin/operator: the exception covers only", false},
		{"a scan that never refuses a finding listed as refused fails", 1, 1, listRefused, accepted + "\n" + acceptedXNet, false},
		{"a scan that accepts a finding listed as refused fails", 1, 1, listRefused, all + "\naccepted CVE-2026-97054 in " + proofBinary + ": the binary's sha256", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, temp := t.TempDir(), t.TempDir()
			summary := filepath.Join(temp, "summary.md")
			writeFile(t, filepath.Join(dir, "scripts", "scan-image.sh"), "printf '%s\\n' \"$STUB_OUT\"\nexit \"$STUB_EXIT\"\n")

			got := kittest.RunWorkflowScript(t, step, kittest.ScriptRun{
				Tools: []string{"bash", "tee", "cat", "grep"},
				Dir:   dir,
				Env: map[string]string{"PROVIDER": "scaleway/scaleway", "VERSION": "2.92.1", "WANT_EXIT": strconv.Itoa(c.want),
					"ACCEPTED": listAccepted, "REFUSED": c.refused,
					"RUNNER_TEMP": temp, "GITHUB_STEP_SUMMARY": summary, "STUB_OUT": c.scanOut, "STUB_EXIT": strconv.Itoa(c.scanExit)},
			})

			if (got.ExitCode == 0) != c.pass {
				t.Errorf("step exit = %d, want it to pass: %v\nstdout:\n%s\nstderr:\n%s", got.ExitCode, c.pass, got.Stdout, got.Stderr)
			}
			if raw, err := os.ReadFile(summary); err != nil || !strings.Contains(string(raw), c.scanOut) {
				t.Errorf("step summary = %q (err %v); want it to hold the scan's output", raw, err)
			}
		})
	}
}
