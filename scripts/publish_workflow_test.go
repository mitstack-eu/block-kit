package scripts_test

// publish-block.yml runs the kit's scan scripts on every event before publish, and hands blockverify what a pull request changes.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func publishWorkflow(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../.github/workflows/publish-block.yml")
	if err != nil {
		t.Fatalf("reading publish-block.yml: %v", err)
	}
	return workflowFields(string(raw))
}

func TestPublishBlockWorkflow_ScansEveryEventBeforePublish(t *testing.T) {
	all := publishWorkflow(t)
	field := func(path string) string {
		if v, ok := all[path]; ok {
			return v
		}
		return "<absent>"
	}
	step := func(job, key, value string) string { return strconv.FormatBool(stepWith(all, job, key, value)) }
	cases := []struct{ name, got, want string }{
		{"the secrets job carries no if, so every event scans", field("jobs.secrets.if"), "<absent>"},
		{"the scripts job carries no if, so every event lints", field("jobs.scripts.if"), "<absent>"},
		{"the secrets job reads contents alone", field("jobs.secrets.permissions.contents"), "read"},
		{"the scripts job reads contents alone", field("jobs.scripts.permissions.contents"), "read"},
		{"the secrets job checks the block out beside the kit", step("secrets", "with.path", "block"), "true"},
		{"the secrets job checks out every commit the block's HEAD reaches", step("secrets", "with.fetch-depth", "0"), "true"},
		{"the secrets job takes its scan from kit-ref", step("secrets", "with.ref", "${{ inputs.kit-ref }}"), "true"},
		{"the secrets job scans the block with the kit's script", step("secrets", "run", "bash block-kit/scripts/scan-secrets.sh block"), "true"},
		{"the scripts job checks the block out beside the kit", step("scripts", "with.path", "block"), "true"},
		{"the scripts job takes its lint from kit-ref", step("scripts", "with.ref", "${{ inputs.kit-ref }}"), "true"},
		{"the scripts job lints the block with the kit's script", step("scripts", "run", "bash block-kit/scripts/lint-scripts.sh block"), "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("publish-block.yml field = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestPublishBlockWorkflow_HandsBlockverifyTheChangedFiles(t *testing.T) {
	all := publishWorkflow(t)
	const flag = `--changed-files "$RUNNER_TEMP/changed-files"`

	run := all[stepOf(all, "conformance", "name", "blockverify (standalone contract check)")+"run"]

	if !strings.Contains(run, flag) {
		t.Errorf("the blockverify step's run =\n%s\nwant it to pass %s", run, flag)
	}
}

func TestPublishBlockWorkflow_ThePullRequestStepListsEveryFileTheMergeChanges(t *testing.T) {
	const name = "Write the base commit's manifest and the changed files"
	prefix := stepOf(publishWorkflow(t), "conformance", "name", name)
	if prefix == "" {
		t.Fatalf("conformance has no step %q", name)
	}
	repo, shas := gitRepo(t,
		change{write: map[string]string{"manifest.yaml": "spec:\n  version: 1.0.0\n", "README.md": "# block\n", "resource.go": "package main\n", "old.go": "package main\n"}},
		change{write: map[string]string{"README.md": "# block, changed\n", "resource.go": "package main\n\nfunc changed() {}\n", "new.go": "package main\n"}, remove: []string{"old.go"}})
	git(t, repo, "remote", "add", "origin", "file://"+repo)
	temp := t.TempDir()
	step := filepath.Join(temp, "step.sh")
	if err := os.WriteFile(step, []byte(publishWorkflow(t)[prefix+"run"]), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-e", step)
	cmd.Dir, cmd.Env = repo, append(hermeticEnv(), "BASE_SHA="+shas[0], "RUNNER_TEMP="+temp)

	out, err := cmd.CombinedOutput()

	if err != nil {
		t.Fatalf("step %q: %v\n%s", name, err, out)
	}
	changed, err := os.ReadFile(filepath.Join(temp, "changed-files"))
	if want := "README.md\x00new.go\x00old.go\x00resource.go\x00"; err != nil || string(changed) != want {
		t.Errorf("changed-files = %q (err %v), want %q, each path NUL-terminated and a moved file under both paths", changed, err, want)
	}
}
