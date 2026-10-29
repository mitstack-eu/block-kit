package landing_test

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing/landingtest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

func waiting(t *testing.T, err error, detail string) {
	t.Helper()
	var w *handler.Waiting
	if !errors.As(err, &w) || !strings.Contains(w.Detail, detail) {
		t.Fatalf("Remove = %v, want a handler.Waiting naming %q, so the unsubscribe stays unsettled and is re-driven", err, detail)
	}
}

func TestUnsubscribe_UnderARequiredCheckReachesMainOnlyThroughItsPullRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		require func(f *landingtest.Fake, check string)
	}{
		{"by branch protection", (*landingtest.Fake).RequireCheck},
		{"by a ruleset", (*landingtest.Fake).RequireCheckByRuleset},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := landingtest.New(t, repo)
			rec := land(t, f, threeFiles, landing.Record{})
			tc.require(f, "managed-ci")
			mark := len(f.Requests())

			_, err := remove(client(f), rec)
			waiting(t, err, "managed-ci")
			_, err = remove(client(f), rec)
			waiting(t, err, "managed-ci")
			if pulls := f.Pulls(); len(pulls) != 1 || !pulls[0].Open {
				t.Fatalf("pull requests while the check has not passed = %+v, want one, open", pulls)
			}
			for _, r := range f.Writes() {
				if r.Method == http.MethodPut {
					t.Fatalf("%s %s asked GitHub to merge before the required check passed", r.Method, r.Path)
				}
			}
			if !reflect.DeepEqual(f.Main(), threeFiles) {
				t.Fatalf("main = %v before the check passed, want it untouched", f.Main())
			}

			f.ReportCheck("managed-ci", "success")
			if _, err := remove(client(f), rec); err != nil {
				t.Fatalf("Remove once the check passed: %v", err)
			}
			if len(f.Main()) != 0 {
				t.Errorf("main = %v, want every landed file removed", f.Main())
			}
			if pulls := f.Pulls(); len(pulls) != 1 || !pulls[0].Merged {
				t.Errorf("pull requests = %+v, want the one removal merged", pulls)
			}
			for _, r := range f.Requests()[mark:] {
				if r.Method != http.MethodGet && strings.HasSuffix(r.Path, "/git/refs/heads/main") {
					t.Errorf("%s %s pushed to main directly, want main reached only through the pull request", r.Method, r.Path)
				}
			}
		})
	}
}

func TestUnsubscribe_WaitsOnARequiredCheckThatFailed(t *testing.T) {
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})
	f.RequireCheck("managed-ci")
	f.ReportCheck("managed-ci", "failure")

	_, err := remove(client(f), rec)
	waiting(t, err, "managed-ci")
	if pulls := f.Pulls(); len(pulls) != 1 || pulls[0].Merged {
		t.Errorf("pull requests = %+v, want one, unmerged, since a failed check never lets it past", pulls)
	}
}

func TestUnsubscribe_LeavesNoBranchUnderTheSubscriptionsPrefix(t *testing.T) {
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})
	f.Branch("mitstack/" + subscription + "/land")
	f.Branch("mitstack/" + subscription + "5/land")
	f.Branch("feature/app-work")

	if _, err := remove(client(f), rec); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if want := []string{"feature/app-work", "mitstack/" + subscription + "5/land"}; !reflect.DeepEqual(f.Branches(), want) {
		t.Errorf("branches = %v, want only those outside the subscription's prefix, %v", f.Branches(), want)
	}
}

func TestUnsubscribe_AnswersAProvenDeletedRepositoryAbsentWithNoWrite(t *testing.T) {
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})
	f.DeleteRepository()
	mark := len(f.Writes())

	if obs := observe(t, client(f), rec); obs.State != contract.Absent {
		t.Errorf("observe = %s (%s), want absent for a repository proven deleted", obs.State, obs.StatusDetail)
	}
	if _, err := remove(client(f), rec); err != nil {
		t.Errorf("Remove = %v, want nothing left to remove", err)
	}
	if w := f.Writes()[mark:]; len(w) != 0 {
		t.Errorf("writes = %+v, want none", w)
	}
	var listed bool
	for _, r := range f.Requests() {
		listed = listed || r.Path == "/installation/repositories?per_page=1" && r.Authorization == "Bearer "+token
	}
	if !listed {
		t.Errorf("requests = %+v, want the installation's repository list read with the same token, since a 404 alone proves no deletion", f.Requests())
	}
}

func TestUnsubscribe_AnswersAnUnreadableRepositoryUnknownWithNoWrite(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *landingtest.Fake)
		token string
	}{
		{"the repository answers 403", func(f *landingtest.Fake) { f.Answer(http.MethodGet, "/repos/"+repo, http.StatusForbidden) }, token},
		{"main's ref answers a rate limit", func(f *landingtest.Fake) {
			f.Answer(http.MethodGet, "/repos/"+repo+"/git/ref/heads/main", http.StatusTooManyRequests)
		}, token},
		{"a landed file's read answers 500", func(f *landingtest.Fake) {
			f.Answer(http.MethodGet, "/repos/"+repo+"/contents/Dockerfile", http.StatusInternalServerError)
		}, token},
		{"the repository answers 404 to a token covering selected repositories", func(f *landingtest.Fake) {
			f.DeleteRepository()
			f.CoverSelectedRepositories()
		}, token},
		{"the repository answers 404 and the installation's list answers 500", func(f *landingtest.Fake) {
			f.DeleteRepository()
			f.Answer(http.MethodGet, "/installation/repositories", http.StatusInternalServerError)
		}, token},
		{"no token", func(*landingtest.Fake) {}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := landingtest.New(t, repo)
			rec := land(t, f, threeFiles, landing.Record{})
			tc.setup(f)
			mark := len(f.Writes())
			c := ghapi.NewClient(f.URL, tc.token, nil)

			if obs := observe(t, c, rec); obs.State != contract.Unknown {
				t.Errorf("observe = %s (%s), want unknown", obs.State, obs.StatusDetail)
			}
			_, err := remove(c, rec)
			waiting(t, err, repo)
			if w := f.Writes()[mark:]; len(w) != 0 {
				t.Errorf("writes = %+v, want none from a read that failed", w)
			}
		})
	}
}
