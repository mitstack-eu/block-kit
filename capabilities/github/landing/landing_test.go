package landing_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing/landingtest"
	"github.com/mitstack-eu/block-kit/contract"
)

const (
	repo         = "acme/weather"
	token        = "ghs_landingtest0001"
	subscription = "sub_641d1fee2774"
)

var threeFiles = map[string]string{"CLAUDE.md": "# weather\n", "Dockerfile": "FROM nginx:1.27\n", "index.html": "<h1>weather</h1>\n"}

func client(f *landingtest.Fake) *ghapi.Client { return ghapi.NewClient(f.URL, token, nil) }

func land(t *testing.T, f *landingtest.Fake, files map[string]string, prior landing.Record) landing.Record {
	t.Helper()
	rec, err := landing.Land(context.Background(), client(f), landing.Landing{Repository: repo, Message: "mitstack: land the starter", Files: files, Prior: prior})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	return rec
}

func remove(c *ghapi.Client, recs ...landing.Record) (landing.Result, error) {
	return landing.Remove(context.Background(), c, landing.Removal{Subscription: subscription, Records: recs})
}

func observe(t *testing.T, c *ghapi.Client, rec landing.Record) contract.Observation {
	t.Helper()
	identity, err := rec.Identity()
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	o := landing.Observer{Client: func(context.Context, contract.Desired) (*ghapi.Client, error) { return c, nil }}
	obs, err := o.ObserveResource(context.Background(), contract.ResourceRef{Name: "starter", Type: "github.scaffold-commit", Identity: identity})
	if err != nil {
		t.Fatalf("ObserveResource: %v", err)
	}
	return obs
}

func TestBlobHash_IsGitsBlobHash(t *testing.T) {
	if got, want := landing.BlobHash(nil), "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"; got != want {
		t.Errorf("BlobHash of the empty file = %s, want git's %s", got, want)
	}
}

func TestLand_RecordsEachFileItCreatedInTheIdentityShapeADR0107Gives(t *testing.T) {
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})

	identity, err := rec.Identity()
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if identity["repository"] != repo {
		t.Errorf(`identity["repository"] = %q, want %q`, identity["repository"], repo)
	}
	var files []map[string]any
	if err := json.Unmarshal([]byte(identity["files"]), &files); err != nil {
		t.Fatalf(`identity["files"] = %q is not a JSON list: %v`, identity["files"], err)
	}
	want := []map[string]any{
		{"path": "CLAUDE.md", "blob": landing.BlobHash([]byte(threeFiles["CLAUDE.md"])), "created": true},
		{"path": "Dockerfile", "blob": landing.BlobHash([]byte(threeFiles["Dockerfile"])), "created": true},
		{"path": "index.html", "blob": landing.BlobHash([]byte(threeFiles["index.html"])), "created": true},
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("recorded files = %v, want %v", files, want)
	}
	back, err := landing.RecordFromIdentity(identity)
	if err != nil || !reflect.DeepEqual(back, rec) {
		t.Errorf("RecordFromIdentity = %+v, %v, want the record it came from, %+v", back, err, rec)
	}
	if !reflect.DeepEqual(f.Main(), threeFiles) {
		t.Errorf("main = %v, want the landed files", f.Main())
	}
}

func TestUnsubscribe_RemovesEveryUnchangedLandedFileAndNamesTheChangedOneKept(t *testing.T) {
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})
	f.Edit("index.html", "<h1>my weather</h1>\n")
	if obs := observe(t, client(f), rec); obs.State != contract.Present {
		t.Fatalf("observe before unsubscribe = %s (%s), want present while main carries two landed files unchanged", obs.State, obs.StatusDetail)
	}

	got, err := remove(client(f), rec)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if want := map[string]string{"index.html": "<h1>my weather</h1>\n"}; !reflect.DeepEqual(f.Main(), want) {
		t.Errorf("main = %v, want only the file the app changed, %v", f.Main(), want)
	}
	if want := []landing.Kept{{Repository: repo, Path: "index.html", Reason: "changed"}}; !reflect.DeepEqual(got.Kept, want) {
		t.Errorf("Kept = %+v, want %+v", got.Kept, want)
	}
	pulls := f.Pulls()
	if len(pulls) != 1 || !pulls[0].Merged || !strings.Contains(pulls[0].Body, "index.html") {
		t.Errorf("pull requests = %+v, want one removal merged whose description names index.html kept", pulls)
	}
	if obs := observe(t, client(f), rec); obs.State != contract.Absent {
		t.Errorf("observe after unsubscribe = %s (%s), want absent, since main carries none of the landed files unchanged", obs.State, obs.StatusDetail)
	}
}

func TestLand_RecordsAPathMainHeldAsNotCreatedAndUnsubscribeLeavesIt(t *testing.T) {
	f := landingtest.New(t, repo)
	f.Seed(map[string]string{"README.md": "# weather\n"})
	rec := land(t, f, map[string]string{"README.md": "# weather\n", "Dockerfile": "FROM nginx:1.27\n"}, landing.Record{})

	want := []landing.File{
		{Path: "Dockerfile", Blob: landing.BlobHash([]byte("FROM nginx:1.27\n")), Created: true},
		{Path: "README.md", Blob: landing.BlobHash([]byte("# weather\n")), Created: false},
	}
	if !reflect.DeepEqual(rec.Files, want) {
		t.Fatalf("recorded files = %+v, want %+v", rec.Files, want)
	}
	if _, err := remove(client(f), rec); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if want := map[string]string{"README.md": "# weather\n"}; !reflect.DeepEqual(f.Main(), want) {
		t.Errorf("main = %v, want README.md byte for byte and nothing else, %v", f.Main(), want)
	}
}

func TestLand_AgainKeepsAPathItCreatedRecordedCreated(t *testing.T) {
	f := landingtest.New(t, repo)
	first := land(t, f, threeFiles, landing.Record{})
	again := land(t, f, threeFiles, first)

	if !reflect.DeepEqual(again, first) {
		t.Errorf("a repeated landing recorded %+v, want the first landing's record %+v, since the block created those paths", again, first)
	}
}
