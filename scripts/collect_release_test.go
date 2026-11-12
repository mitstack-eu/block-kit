package scripts_test

// collect-release.sh empties a directory and fills it with the files a release signs, the block's publisher workflow where it holds one, and the image saved as image.tar, and fails naming a file that is missing.

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCollectRelease(t *testing.T) {
	goBlock := map[string]string{"manifest.yaml": "spec:\n  version: 1.2.0\n", "go.mod": "module block\n", "blockverify-report.json": `{"verdict":"pass"}`, "main.go": "package main\n"}
	withPublisher := maps.Clone(goBlock)
	withPublisher[".github/workflows/release-publisher.yml"] = "name: release-publisher\n"
	withPublisher[".github/workflows/ci.yml"] = "name: ci\n"
	unjudged := maps.Clone(goBlock)
	delete(unjudged, "blockverify-report.json")
	named := []string{"manifest.yaml", "go.mod", "blockverify-report.json"}
	cases := []struct {
		name     string
		tree     map[string]string
		files    []string
		wantExit bool
		wantOut  string
		want     []string
	}{
		{"a block holding the publisher hands over each named file, that workflow alone and the image", withPublisher, named, true, "",
			[]string{".github/workflows/release-publisher.yml", "blockverify-report.json", "go.mod", "image.tar", "manifest.yaml"}},
		{"a block holding no publisher hands over no workflow", goBlock, named, true, "", []string{"blockverify-report.json", "go.mod", "image.tar", "manifest.yaml"}},
		{"a file named outside the tree lands under its own name", goBlock, []string{"manifest.yaml", "OUTSIDE/block-verify.txt"}, true, "", []string{"block-verify.txt", "image.tar", "manifest.yaml"}},
		{"a named file that is missing fails naming it, and no image is saved", unjudged, named, false, "blockverify-report.json is missing", []string{"go.mod", "manifest.yaml"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, log := stubbedDocker(t)
			tree, outside, dir := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "block-release")
			for path, content := range tc.tree {
				writeFile(t, filepath.Join(tree, path), content)
			}
			writeFile(t, filepath.Join(outside, "block-verify.txt"), "PASS\n")
			writeFile(t, filepath.Join(dir, ".trivyignore"), "planted before the collection\n")
			writeFile(t, filepath.Join(dir, "image.tar"), "planted before the collection\n")
			script, err := filepath.Abs("collect-release.sh")
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"-c", `cd "$1" && shift && bash "$@"`, "collect", tree, script, "block:c0ffee", dir, "release-publisher.yml"}
			for _, file := range tc.files {
				args = append(args, strings.ReplaceAll(file, "OUTSIDE", outside))
			}

			out, exit := runBash(t, env, args...)

			if (exit == 0) != tc.wantExit || !strings.Contains(out, tc.wantOut) {
				t.Fatalf("exit %d, output:\n%s\nwant success %v and output naming %q", exit, out, tc.wantExit, tc.wantOut)
			}
			var got []string
			_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					rel, _ := filepath.Rel(dir, path)
					got = append(got, rel)
				}
				return nil
			})
			if !slices.Equal(got, tc.want) {
				t.Errorf("the directory holds %q, want %q, and nothing planted there before", got, tc.want)
			}
			wantDocker, wantImage := "", ""
			if tc.wantExit {
				wantDocker, wantImage = "docker save --output "+filepath.Join(dir, "image.tar")+" block:c0ffee", "saved image\n"
			}
			if got := dockerCalls(t, log); got != wantDocker {
				t.Errorf("docker calls = %q, want %q", got, wantDocker)
			}
			if image, _ := os.ReadFile(filepath.Join(dir, "image.tar")); string(image) != wantImage {
				t.Errorf("image.tar = %q, want %q", image, wantImage)
			}
			if manifest, _ := os.ReadFile(filepath.Join(dir, "manifest.yaml")); string(manifest) != tc.tree["manifest.yaml"] {
				t.Errorf("manifest.yaml = %q, want the tree's own %q", manifest, tc.tree["manifest.yaml"])
			}
		})
	}
}
