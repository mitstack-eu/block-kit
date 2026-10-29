package landing_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing/landingtest"
	"github.com/mitstack-eu/block-kit/contract"
)

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
