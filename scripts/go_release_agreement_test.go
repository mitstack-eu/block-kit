package scripts_test

// After a Go move that edits one place only, the suite names each kit workflow and Dockerfile left on another release than publish-block.yml installs by default.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	goReleaseFile = regexp.MustCompile(`(^|/)(\.github/workflows/[^/]+\.ya?ml|Dockerfile)$`)
	goReleaseLine = regexp.MustCompile(`^\s*(?:go-version:\s*['"]?|FROM\s+(?:--\S+\s+)*(?:\S+/)?golang:)(\d+(?:\.\d+)*)`)
)

func TestGoRelease_EveryWorkflowAndDockerfileNamesThePublishDefault(t *testing.T) {
	want := strings.Trim(publishWorkflow(t)["on.workflow_call.inputs.go-version.default"], `"`)
	workflows, images := 0, 0

	// A fixture block under testdata stands for a builder's repository, which names its own Go.
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata"):
			return filepath.SkipDir
		case d.IsDir() || !goReleaseFile.MatchString(filepath.ToSlash(path)):
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			m := goReleaseLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if d.Name() == "Dockerfile" {
				images++
			} else {
				workflows++
			}
			if m[1] != want {
				t.Errorf("%s:%d names Go %s; publish-block.yml installs Go %q by default", strings.TrimPrefix(filepath.ToSlash(path), "../"), i+1, m[1], want)
			}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("reading the kit's workflows and Dockerfiles: %v", err)
	}
	if workflows == 0 || images == 0 {
		t.Errorf("found %d setup-go releases and %d golang images; want at least one of each, since a check that reads none compares nothing", workflows, images)
	}
}
