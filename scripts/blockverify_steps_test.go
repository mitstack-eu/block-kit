package scripts_test

// write-pr-base.sh writes a pull request's base manifest and every file its merge changes, and run-blockverify.sh hands them to blockverify on a pull request alone and fails on any verdict but pass.

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWritePRBase_WritesTheBaseManifestAndEveryFileTheMergeChanges(t *testing.T) {
	script, err := filepath.Abs("write-pr-base.sh")
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	edited := []change{
		{write: map[string]string{"manifest.yaml": "spec:\n  version: 1.0.0\n", "README.md": "# block\n", "resource.go": "package main\n", "old.go": "package main\n"}},
		{write: map[string]string{"README.md": "# block, changed\n", "resource.go": "package main\n\nfunc changed() {}\n", "new.go": "package main\n"}, remove: []string{"old.go"}},
	}
	const editedFiles, baseManifest = "README.md\x00new.go\x00old.go\x00resource.go\x00", "spec:\n  version: 1.0.0\n"
	cases := []struct {
		name, token      string
		history          []change
		changed, written string
	}{
		{"a block with a base manifest gets it and every changed path, a removed file included", "", edited, editedFiles, baseManifest},
		{"a fetch given a token hands git its header through the environment", "ghs_test", edited, editedFiles, baseManifest},
		{"a block new on the branch gets no base manifest", "", []change{writes("README.md", "# block\n"),
			{write: map[string]string{"manifest.yaml": "spec:\n  version: 0.1.0\n", "main.tf": "locals {}\n"}}}, "main.tf\x00manifest.yaml\x00", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, shas := gitRepo(t, tc.history...)
			git(t, repo, "remote", "add", "origin", "file://"+repo)
			out, bin := t.TempDir(), t.TempDir()
			fetchLog := filepath.Join(bin, "fetch.log")
			writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\ncase \" $* \" in *\" fetch \"*) printf '%s\\n%s\\n' \"$*\" \"$AUTH_HEADER\" > \""+fetchLog+"\";; esac\nexec \""+realGit+"\" \"$@\"\n")
			env := []string{"FETCH_TOKEN=" + tc.token, "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}

			log, exit := runBash(t, env, "-c", `cd "$1" && bash "$2" "$3" "$4"`, "write", repo, script, shas[0], out)

			if exit != 0 {
				t.Fatalf("write-pr-base.sh exit %d:\n%s", exit, log)
			}
			wantHeader := ""
			if tc.token != "" {
				wantHeader = "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tc.token))
			}
			fetched, err := os.ReadFile(fetchLog)
			args, header, _ := strings.Cut(strings.TrimSuffix(string(fetched), "\n"), "\n")
			if err != nil || header != wantHeader || strings.Contains(args, "--config-env=http.extraheader=AUTH_HEADER") != (tc.token != "") {
				t.Errorf("git fetch ran with %q and AUTH_HEADER %q (err %v); want AUTH_HEADER %q, handed over by --config-env exactly when a token is set", args, header, err, wantHeader)
			}
			if changed, err := os.ReadFile(filepath.Join(out, "changed-files")); err != nil || string(changed) != tc.changed {
				t.Errorf("changed-files = %q (err %v), want %q, each path NUL-terminated", changed, err, tc.changed)
			}
			written, err := os.ReadFile(filepath.Join(out, "base-manifest.yaml"))
			switch {
			case tc.written == "" && (!errors.Is(err, fs.ErrNotExist) || !strings.Contains(log, "new on this branch")):
				t.Errorf("base-manifest.yaml err = %v, log:\n%s\nwant it absent, which blockverify reads as a new block, and the log saying so", err, log)
			case tc.written != "" && (err != nil || string(written) != tc.written):
				t.Errorf("base-manifest.yaml = %q (err %v), want the base commit's manifest %q", written, err, tc.written)
			}
		})
	}
}

func TestRunBlockverify_PassesThePullRequestFilesOnAPullRequestAloneAndFailsOnAnyVerdictButPass(t *testing.T) {
	for _, event := range []string{"pull_request", "push"} {
		for _, verdict := range []string{"pass", "fail"} {
			t.Run(event+" answering "+verdict, func(t *testing.T) {
				temp := t.TempDir()
				blockverify := filepath.Join(temp, "blockverify")
				writeFile(t, blockverify, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+filepath.Join(temp, "args")+"\"\necho '{\"verdict\":\""+verdict+"\"}'\n")

				out, exit := runBash(t, nil, "run-blockverify.sh", blockverify, filepath.Join(temp, "report.json"), event, temp, "--opentofu-only")

				raw, err := os.ReadFile(filepath.Join(temp, "args"))
				if err != nil {
					t.Fatalf("blockverify never ran: %v\n%s", err, out)
				}
				want := []string{"--standalone", "--json", "--opentofu-only"}
				if event == "pull_request" {
					want = append(want, "--base-manifest", filepath.Join(temp, "base-manifest.yaml"), "--changed-files", filepath.Join(temp, "changed-files"))
				}
				if args := strings.Fields(string(raw)); !slices.Equal(args, append(want, ".")) {
					t.Errorf("blockverify ran with %q; want %q", args, append(want, "."))
				}
				if (exit == 0) != (verdict == "pass") || !strings.Contains(out, `"verdict":"`+verdict+`"`) {
					t.Errorf("exit %d, output:\n%s\nwant the report printed and the run to fail exactly when the verdict is not pass", exit, out)
				}
			})
		}
	}
}
