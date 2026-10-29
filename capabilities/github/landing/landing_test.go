package landing_test

import (
	"context"
	"encoding/json"
	"net/http"
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

func offer(t *testing.T, f *landingtest.Fake, files map[string]string, prior landing.Record) landing.Record {
	t.Helper()
	rec, err := landing.Offer(context.Background(), client(f), landing.Landing{Repository: repo, Files: files, Prior: prior})
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	return rec
}

func observeOffer(t *testing.T, f *landingtest.Fake, rec landing.Record, unsubscribing bool) contract.Observation {
	t.Helper()
	identity, err := rec.Identity()
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	o := landing.OfferObserver{Client: func(context.Context, contract.Desired) (*ghapi.Client, error) { return client(f), nil }}
	obs, err := o.ObserveResource(context.Background(), contract.ResourceRef{Name: "contribution", Type: "github.scaffold-contribution", Identity: identity, Unsubscribing: unsubscribing})
	if err != nil {
		t.Fatalf("ObserveResource: %v", err)
	}
	return obs
}

func TestOffer_RecordsWhatALandingWouldWithoutWritingToTheRepository(t *testing.T) {
	f := landingtest.New(t, repo)
	f.Seed(map[string]string{"CLAUDE.md": "# the app's own\n"})

	rec := offer(t, f, threeFiles, landing.Record{})

	want := []landing.File{
		{Path: "CLAUDE.md", Blob: landing.BlobHash([]byte(threeFiles["CLAUDE.md"])), Created: false},
		{Path: "Dockerfile", Blob: landing.BlobHash([]byte(threeFiles["Dockerfile"])), Created: true},
		{Path: "index.html", Blob: landing.BlobHash([]byte(threeFiles["index.html"])), Created: true},
	}
	if rec.Repository != repo || len(rec.Files) != len(want) {
		t.Fatalf("Offer = %+v, want %s with files %+v", rec, repo, want)
	}
	for i := range want {
		if rec.Files[i] != want[i] {
			t.Errorf("file %d = %+v, want %+v", i, rec.Files[i], want[i])
		}
	}
	if w := f.Writes(); len(w) != 0 {
		t.Errorf("Offer sent writes %+v, want none, since another block commits the files", w)
	}
}

func TestOffer_AgainOnceTheFilesAreOnMainKeepsThePathsItCreatedRecordedCreated(t *testing.T) {
	f := landingtest.New(t, repo)
	first := offer(t, f, threeFiles, landing.Record{})
	f.Seed(threeFiles)

	again := offer(t, f, threeFiles, first)

	for _, file := range again.Files {
		if !file.Created {
			t.Errorf("file %s recorded not created after the files reached main, want created as the first offer recorded it", file.Path)
		}
	}
}

func TestOfferObserver_OutsideAnUnsubscribeAnswersPresentWhileTheRepositoryCanBeRead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(f *landingtest.Fake) landing.Record
	}{
		{"an empty record", func(*landingtest.Fake) landing.Record { return landing.Record{Repository: repo} }},
		{"files not yet on main", func(f *landingtest.Fake) landing.Record { return offer(t, f, threeFiles, landing.Record{}) }},
		{"files on main unchanged", func(f *landingtest.Fake) landing.Record {
			rec := offer(t, f, threeFiles, landing.Record{})
			f.Seed(threeFiles)
			return rec
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := landingtest.New(t, repo)
			rec := tc.arrange(f)

			obs := observeOffer(t, f, rec, false)

			if obs.State != contract.Present {
				t.Errorf("observe outside an unsubscribe = %s (%s), want present while %s can be read", obs.State, obs.StatusDetail, repo)
			}
		})
	}
}

func TestOfferObserver_DuringAnUnsubscribeIsPresentOnlyWhileMainCarriesAFileItCreatedUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(f *landingtest.Fake) landing.Record
		want    contract.State
		naming  string
	}{
		{"one created file on main unchanged", func(f *landingtest.Fake) landing.Record {
			rec := offer(t, f, threeFiles, landing.Record{})
			f.Seed(map[string]string{"Dockerfile": threeFiles["Dockerfile"]})
			return rec
		}, contract.Present, "Dockerfile"},
		{"none of its files ever on main", func(f *landingtest.Fake) landing.Record { return offer(t, f, threeFiles, landing.Record{}) }, contract.Absent, ""},
		{"only a changed file on main", func(f *landingtest.Fake) landing.Record {
			rec := offer(t, f, threeFiles, landing.Record{})
			f.Seed(map[string]string{"index.html": "<h1>the app's own</h1>\n"})
			return rec
		}, contract.Absent, ""},
		{"an empty record", func(*landingtest.Fake) landing.Record { return landing.Record{Repository: repo} }, contract.Absent, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := landingtest.New(t, repo)
			rec := tc.arrange(f)

			obs := observeOffer(t, f, rec, true)

			if obs.State != tc.want || !strings.Contains(obs.StatusDetail, tc.naming) {
				t.Errorf("observe during an unsubscribe = %s (%s), want %s naming %q", obs.State, obs.StatusDetail, tc.want, tc.naming)
			}
		})
	}
}

func TestOfferObserver_ReadsADeletedRepositoryAbsentAndAnUnreadableOneUnknownEitherWay(t *testing.T) {
	for _, unsubscribing := range []bool{false, true} {
		deleted := landingtest.New(t, repo)
		rec := offer(t, deleted, threeFiles, landing.Record{})
		deleted.DeleteRepository()
		if obs := observeOffer(t, deleted, rec, unsubscribing); obs.State != contract.Absent {
			t.Errorf("unsubscribing %v: observe of a deleted repository = %s (%s), want absent", unsubscribing, obs.State, obs.StatusDetail)
		}

		unreadable := landingtest.New(t, repo)
		unreadable.Answer(http.MethodGet, "/repos/"+repo, http.StatusInternalServerError)
		if obs := observeOffer(t, unreadable, rec, unsubscribing); obs.State != contract.Unknown {
			t.Errorf("unsubscribing %v: observe of a repository answering 500 = %s (%s), want unknown", unsubscribing, obs.State, obs.StatusDetail)
		}
	}
}
