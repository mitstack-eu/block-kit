package scripts_test

// scan-image-proof.yml's scan step passes only when scan-image.sh exits as the matrix expects, accepts GO-2026-5932 in the provider and refuses it nowhere, and records the scan in the step summary.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/verify/kittest"
)

const accepted = "accepted GO-2026-5932 in var/cache/tofu-plugins/registry.opentofu.org/scaleway/scaleway/2.89.0/linux_amd64/terraform-provider-scaleway_v2.89.0: its Go function table names 324"

func TestScanImageProof_PassesOnlyWhenTheScanExitsAsExpectedAndAcceptsTheAdvisory(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "scan-image-proof.yml"))
	if err != nil {
		t.Fatal(err)
	}
	step, err := kittest.ReadWorkflowScript(workflow, "real-provider", "Scan the image, expecting GO-2026-5932 accepted and exit ${{ matrix.exit }}")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name           string
		scanExit, want int
		scanOut        string
		pass           bool
	}{
		{"a scan exiting 1 as expected on refusals other than GO-2026-5932 passes", 1, 1, accepted + "\nrefused CVE-2026-78669 in x: golang.org/x/net v0.59.0 has a fix in 0.60.0", true},
		{"a scan exiting 0 as expected passes", 0, 0, accepted, true},
		{"a scan exiting 1 where 0 is expected fails", 1, 0, accepted, false},
		{"a scan exiting 0 where 1 is expected fails", 0, 1, accepted, false},
		{"a scan that never accepts GO-2026-5932 fails", 1, 1, "refused CVE-2026-78669 in x", false},
		{"a scan that refuses GO-2026-5932 anywhere fails", 1, 1, accepted + "\nrefused GO-2026-5932 in usr/local/bin/operator: the exception covers only", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, temp := t.TempDir(), t.TempDir()
			summary := filepath.Join(temp, "summary.md")
			writeFile(t, filepath.Join(dir, "scripts", "scan-image.sh"), "printf '%s\\n' \"$STUB_OUT\"\nexit \"$STUB_EXIT\"\n")

			got := kittest.RunWorkflowScript(t, step, kittest.ScriptRun{
				Tools: []string{"bash", "tee", "cat", "grep"},
				Dir:   dir,
				Env: map[string]string{"PROVIDER": "scaleway/scaleway", "VERSION": "2.89.0", "WANT_EXIT": strconv.Itoa(c.want),
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
