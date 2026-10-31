package landingtest_test

// The fake lists main's tree, names its head, removes files from it, and lets a test act before each write.

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing/landingtest"
)

const repo = "acme/host"

func client(f *landingtest.Fake) *ghapi.Client {
	return ghapi.NewClient(f.URL, "ghs_test-token", f.Client())
}

func TestFake_ListsMainsTreeRecursivelyAndNamesItsHead(t *testing.T) {
	f := landingtest.New(t, repo)
	f.Seed(map[string]string{"shop/compose.yaml": "{}\n", "README.md": "host\n"})
	c := client(f)

	head, err := c.HeadCommit(context.Background(), repo)
	if err != nil || head != f.Head() {
		t.Fatalf("HeadCommit = %q, %v; want the fake's head %q", head, err, f.Head())
	}
	tree, err := c.CommitTree(context.Background(), repo, head)
	if err != nil {
		t.Fatalf("CommitTree(%s) = %v", head, err)
	}
	paths, err := c.TreePaths(context.Background(), repo, tree)

	if want := []string{"README.md", "shop/compose.yaml"}; err != nil || !slices.Equal(paths, want) {
		t.Errorf("TreePaths = %q, %v; want %q", paths, err, want)
	}
}

func TestFake_RemoveTakesFilesOffMainInANewCommit(t *testing.T) {
	f := landingtest.New(t, repo)
	f.Seed(map[string]string{"shop/compose.yaml": "{}\n", "blog/compose.yaml": "{}\n"})
	before := f.Head()

	f.Remove("shop/compose.yaml", "never/held.txt")

	if got, want := f.Main(), map[string]string{"blog/compose.yaml": "{}\n"}; !maps.Equal(got, want) {
		t.Errorf("Main after Remove = %v; want %v", got, want)
	}
	if f.Head() == before {
		t.Error("Remove left main's head where it was; want a new commit")
	}
}

func TestFake_BeforeWriteRefusesTheWriteItErrsOn(t *testing.T) {
	f := landingtest.New(t, repo)
	f.Seed(map[string]string{"README.md": "host\n"})
	var asked []string
	f.BeforeWrite(func(method, path string) error {
		asked = append(asked, method+" "+path)
		if method == http.MethodPost && path == "/repos/"+repo+"/git/trees" {
			return errors.New("broken by the test")
		}
		return nil
	})
	content := "{}\n"

	_, err := client(f).CommitChanges(context.Background(), repo, f.Head(), "lands shop", map[string]*string{"shop/compose.yaml": &content})

	if err == nil || len(f.Main()) != 1 {
		t.Errorf("CommitChanges with its tree refused = %v, leaving %v; want an error and main unchanged", err, f.Main())
	}
	if want := []string{"POST /repos/" + repo + "/git/trees"}; !slices.Equal(asked, want) {
		t.Errorf("BeforeWrite was asked about %q; want %q, and never about a read", asked, want)
	}
}

func TestFake_BeforeWriteMayMoveMainSoTheCommitIsNotAFastForward(t *testing.T) {
	f := landingtest.New(t, repo)
	f.Seed(map[string]string{"README.md": "host\n"})
	moved := false
	f.BeforeWrite(func(method, _ string) error {
		if method == http.MethodPatch && !moved {
			moved = true
			f.Seed(map[string]string{"blog/compose.yaml": "{}\n"})
		}
		return nil
	})
	content := "{}\n"

	_, err := client(f).CommitChanges(context.Background(), repo, f.Head(), "lands shop", map[string]*string{"shop/compose.yaml": &content})

	if !errors.Is(err, ghapi.ErrNotFastForward) {
		t.Errorf("CommitChanges with main moved under it = %v; want ErrNotFastForward", err)
	}
	if _, landed := f.Main()["blog/compose.yaml"]; !landed {
		t.Errorf("main = %v; want the change BeforeWrite landed", f.Main())
	}
}
