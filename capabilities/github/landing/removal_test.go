package landing_test

import (
	"context"
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

// The platform reads the trailer to start no build for the merge and to still check the teardown's own pull request.
func TestUnsubscribe_TheRemovalCommitAndItsSquashCarryTheSubscriptionTrailer(t *testing.T) {
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})
	f.RequireCheck("managed-ci")
	_, err := remove(client(f), rec)
	waiting(t, err, "managed-ci")
	head := f.Message("mitstack/" + subscription + "/remove")
	f.ReportCheck("managed-ci", "success")

	if _, err := remove(client(f), rec); err != nil {
		t.Fatalf("Remove once the check passed: %v", err)
	}

	trailer := "Mitstack-Subscription: " + subscription
	for name, message := range map[string]string{"the pull request's head commit": head, "the squash merged onto main": f.Message("main")} {
		paragraphs := strings.Split(message, "\n\n")
		if len(paragraphs) < 2 || paragraphs[len(paragraphs)-1] != trailer {
			t.Errorf("%s has message %q, want its last paragraph to be the trailer %q", name, message, trailer)
		}
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

func TestUnsubscribe_RebuildsItsRemovalOntoMainWhenTheAppChangesAFileWhileItWaits(t *testing.T) {
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})
	f.RequireCheck("managed-ci")
	_, err := remove(client(f), rec)
	waiting(t, err, "managed-ci")

	f.Edit("index.html", "<h1>my weather</h1>\n")
	f.ReportCheck("managed-ci", "success")
	got, err := remove(client(f), rec)
	if err != nil {
		t.Fatalf("Remove after main moved: %v, want the removal rebuilt onto main's new head and merged", err)
	}
	if want := map[string]string{"index.html": "<h1>my weather</h1>\n"}; !reflect.DeepEqual(f.Main(), want) {
		t.Errorf("main = %v, want only the file the app changed while the removal waited, %v", f.Main(), want)
	}
	if want := []landing.Kept{{Repository: repo, Path: "index.html", Reason: "changed"}}; !reflect.DeepEqual(got.Kept, want) {
		t.Errorf("Kept = %+v, want %+v", got.Kept, want)
	}
}

func TestUnsubscribe_WaitsWhileGitHubRefusesTheMerge(t *testing.T) {
	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := landingtest.New(t, repo)
			rec := land(t, f, threeFiles, landing.Record{})
			f.Answer(http.MethodPut, "/repos/"+repo+"/pulls/1/merge", status)

			_, err := remove(client(f), rec)
			waiting(t, err, "#1")
			if pulls := f.Pulls(); len(pulls) != 1 || !pulls[0].Open {
				t.Errorf("pull requests = %+v, want the removal left open", pulls)
			}
			if want := []string{"mitstack/" + subscription + "/remove"}; !reflect.DeepEqual(f.Branches(), want) {
				t.Errorf("branches = %v, want the removal's branch kept while it waits, %v", f.Branches(), want)
			}
		})
	}
}

func TestUnsubscribe_OpensTheRemovalAgainWhenSomeoneClosedIt(t *testing.T) {
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})
	f.RequireCheck("managed-ci")
	_, err := remove(client(f), rec)
	waiting(t, err, "managed-ci")
	f.ClosePull(1)
	f.ReportCheck("managed-ci", "success")

	if _, err := remove(client(f), rec); err != nil {
		t.Fatalf("Remove after the removal was closed: %v", err)
	}
	if pulls := f.Pulls(); len(pulls) != 2 || pulls[0].Merged || !pulls[1].Merged {
		t.Errorf("pull requests = %+v, want the closed one left and a second merged", pulls)
	}
	if len(f.Main()) != 0 {
		t.Errorf("main = %v, want every landed file removed", f.Main())
	}
}

func TestUnsubscribe_WaitsOnARequiredCheckAsARemovalWait(t *testing.T) {
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})
	f.RequireCheck("managed-ci")
	f.RunCheck("managed-ci", "in_progress")

	_, err := remove(client(f), rec)

	var w *handler.Waiting
	if !errors.As(err, &w) || !w.Removal {
		t.Errorf("Remove while its required check runs = %#v, want a handler.Waiting with Removal set, so the unsubscribe answer carries each resource's observe", err)
	}
}

// waitingRemoval lands the starter under a required managed-ci check and leaves its removal pull request #1 open on it.
func waitingRemoval(t *testing.T) (*landingtest.Fake, landing.Record) {
	t.Helper()
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})
	f.RequireCheck("managed-ci")
	_, err := remove(client(f), rec)
	waiting(t, err, "managed-ci")
	return f, rec
}

func observeRemoval(t *testing.T, f *landingtest.Fake, rec landing.Record, unsubscribing bool) contract.Observation {
	t.Helper()
	identity, err := rec.Identity()
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	o := landing.Observer{Client: func(context.Context, contract.Desired) (*ghapi.Client, error) { return client(f), nil }}
	ref := contract.ResourceRef{Name: "starter", Type: "github.scaffold-commit", Identity: identity, Unsubscribing: unsubscribing,
		Desired: contract.Desired{Identity: contract.Identity{SubscriptionID: subscription}}}
	obs, err := o.ObserveResource(context.Background(), ref)
	if err != nil {
		t.Fatalf("ObserveResource: %v", err)
	}
	return obs
}

func TestObserver_ReadsTheRemovalUnderWayWhileItsPullRequestsRequiredCheckIsQueuedOrRunning(t *testing.T) {
	for _, status := range []string{"queued", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			f, rec := waitingRemoval(t)
			f.RunCheck("managed-ci", status)

			obs := observeRemoval(t, f, rec, true)

			if obs.State != contract.Present || obs.Removing == nil {
				t.Fatalf("observe while the check is %s = %s with removing %+v (%s), want present with removing", status, obs.State, obs.Removing, obs.StatusDetail)
			}
			for _, naming := range []string{"#1", repo, "managed-ci"} {
				if !strings.Contains(obs.Removing.WaitsOn, naming) {
					t.Errorf("waitsOn = %q, want it to name %q", obs.Removing.WaitsOn, naming)
				}
			}
		})
	}
}

func TestObserver_ReadsNoRemovalUnderWayOnceNothingMovesIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(f *landingtest.Fake)
	}{
		{"its required check failed", func(f *landingtest.Fake) { f.ReportCheck("managed-ci", "failure") }},
		{"its required check failed while a review is also required", func(f *landingtest.Fake) {
			f.RequireReviews(1)
			f.ReportCheck("managed-ci", "failure")
		}},
		{"one required check failed while another runs", func(f *landingtest.Fake) {
			f.RunCheck("managed-ci", "in_progress")
			f.RequireCheck("lint")
			f.ReportCheck("lint", "failure")
		}},
		{"it closed unmerged while its check runs", func(f *landingtest.Fake) {
			f.RunCheck("managed-ci", "in_progress")
			f.ClosePull(1)
		}},
		{"its required check has not started", func(*landingtest.Fake) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, rec := waitingRemoval(t)
			tc.arrange(f)

			obs := observeRemoval(t, f, rec, true)

			if obs.State != contract.Present || obs.Removing != nil {
				t.Errorf("observe once %s = %s with removing %+v (%s), want present with no removing, so the attempt budget applies", tc.name, obs.State, obs.Removing, obs.StatusDetail)
			}
		})
	}
}

func TestObserver_OutsideAnUnsubscribeReadsNoRemovalAndNoPullRequest(t *testing.T) {
	f, rec := waitingRemoval(t)
	f.RunCheck("managed-ci", "in_progress")
	mark := len(f.Requests())

	obs := observeRemoval(t, f, rec, false)

	if obs.State != contract.Present || obs.Removing != nil {
		t.Errorf("observe outside an unsubscribe = %s with removing %+v, want present with no removing", obs.State, obs.Removing)
	}
	for _, r := range f.Requests()[mark:] {
		if strings.Contains(r.Path, "/pulls") {
			t.Errorf("observe outside an unsubscribe called %s %s, want no pull request read", r.Method, r.Path)
		}
	}
}

func TestObserver_ReadsAnUnreadableRemovalPullRequestUnknown(t *testing.T) {
	f, rec := waitingRemoval(t)
	f.RunCheck("managed-ci", "in_progress")
	f.Answer(http.MethodGet, "/repos/"+repo+"/pulls", http.StatusInternalServerError)

	obs := observeRemoval(t, f, rec, true)

	if obs.State != contract.Unknown {
		t.Errorf("observe while the removal pull request cannot be read = %s (%s), want unknown, since a failed read proves nothing", obs.State, obs.StatusDetail)
	}
}

// reviewHeldRemoval lands the starter under a required review and leaves its removal pull request #1 open, which GitHub holds back for approvals.
func reviewHeldRemoval(t *testing.T, approvals int) (*landingtest.Fake, landing.Record) {
	t.Helper()
	f := landingtest.New(t, repo)
	rec := land(t, f, threeFiles, landing.Record{})
	f.RequireReviews(approvals)
	_, err := remove(client(f), rec)
	waiting(t, err, "#1")
	return f, rec
}

func TestObserver_ReadsARemovalHeldBackForAReviewAsRemovingNamingTheReview(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(f *landingtest.Fake)
		naming  []string
	}{
		{"no review given", func(*landingtest.Fake) {}, []string{"an approving review"}},
		{"only a ruleset requires the review", func(f *landingtest.Fake) {
			f.RequireReviews(0)
			f.RequireReviewsByRuleset(1)
		}, []string{"an approving review"}},
		{"a review given and a reviewer who then requested changes", func(f *landingtest.Fake) {
			f.Review(1, "ana", "APPROVED")
			f.Review(1, "bo", "CHANGES_REQUESTED")
		}, []string{"an approving review", "requested changes"}},
		{"its required check passed", func(f *landingtest.Fake) {
			f.RequireCheck("managed-ci")
			f.ReportCheck("managed-ci", "success")
		}, []string{"an approving review"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, rec := reviewHeldRemoval(t, 1)
			tc.arrange(f)

			obs := observeRemoval(t, f, rec, true)

			if obs.State != contract.Present || obs.Removing == nil {
				t.Fatalf("observe with %s = %s with removing %+v (%s), want present with removing", tc.name, obs.State, obs.Removing, obs.StatusDetail)
			}
			for _, naming := range append([]string{"#1", repo}, tc.naming...) {
				if !strings.Contains(obs.Removing.WaitsOn, naming) {
					t.Errorf("waitsOn = %q, want it to name %q", obs.Removing.WaitsOn, naming)
				}
			}
		})
	}
}

func TestObserver_ReadsNoRemovingWhenNoReviewHoldsTheRemovalBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(f *landingtest.Fake)
	}{
		{"the required review was given", func(f *landingtest.Fake) { f.Review(1, "ana", "APPROVED") }},
		{"a reviewer approved, then another only commented", func(f *landingtest.Fake) {
			f.Review(1, "ana", "APPROVED")
			f.Review(1, "bo", "COMMENTED")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, rec := reviewHeldRemoval(t, 1)
			tc.arrange(f)

			obs := observeRemoval(t, f, rec, true)

			if obs.State != contract.Present || obs.Removing != nil {
				t.Errorf("observe once %s = %s with removing %+v (%s), want present with no removing", tc.name, obs.State, obs.Removing, obs.StatusDetail)
			}
		})
	}
	t.Run("main requires no review", func(t *testing.T) {
		f, rec := waitingRemoval(t)
		f.ReportCheck("managed-ci", "success")

		obs := observeRemoval(t, f, rec, true)

		if obs.State != contract.Present || obs.Removing != nil {
			t.Errorf("observe with no review required = %s with removing %+v (%s), want present with no removing", obs.State, obs.Removing, obs.StatusDetail)
		}
	})
}

func TestObserver_ReadsUnknownNamingTheReviewReadThatFailed(t *testing.T) {
	for _, path := range []string{"/branches/main/protection", "/rules/branches/main", "/pulls/1/reviews"} {
		t.Run(path, func(t *testing.T) {
			f, rec := reviewHeldRemoval(t, 1)
			f.Answer(http.MethodGet, "/repos/"+repo+path, http.StatusInternalServerError)

			obs := observeRemoval(t, f, rec, true)

			if obs.State != contract.Unknown || !strings.Contains(obs.StatusDetail, path) {
				t.Errorf("observe while GET %s fails = %s (%s), want unknown naming the read, since a failed read proves nothing", path, obs.State, obs.StatusDetail)
			}
		})
	}
}

func TestUnsubscribe_WaitsAsARemovalWaitWhileItsCheckRunsAndGitHubRefusesTheStatusRead(t *testing.T) {
	for _, status := range []string{"queued", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			f, rec := waitingRemoval(t)
			f.RunCheck("managed-ci", status)
			f.RefuseCommitStatus()

			_, err := remove(client(f), rec)

			var w *handler.Waiting
			if !errors.As(err, &w) || !w.Removal {
				t.Errorf("Remove while its check is %s and the status read answers 403 = %#v, want a handler.Waiting with Removal set, so the wait spends no attempt", status, err)
			}
		})
	}
}

func TestUnsubscribe_NamesThePermissionAndTheInstallationWhenGitHubRefusesTheStatusRead(t *testing.T) {
	f, rec := waitingRemoval(t)
	f.RefuseCommitStatus()

	_, err := remove(client(f), rec)

	var w *handler.Waiting
	if !errors.As(err, &w) {
		t.Fatalf("Remove with no check reported and the status read refused = %v, want a handler.Waiting", err)
	}
	for _, naming := range []string{"the installation of acme lacks the permission statuses=read", `"managed-ci"`} {
		if !strings.Contains(w.Detail, naming) {
			t.Errorf("wait detail = %q, want it to name %q", w.Detail, naming)
		}
	}
}

func TestUnsubscribe_WaitsNamingTheFailedCheckWhenGitHubRefusesTheStatusRead(t *testing.T) {
	f, rec := waitingRemoval(t)
	f.ReportCheck("managed-ci", "failure")
	f.RefuseCommitStatus()

	_, err := remove(client(f), rec)

	waiting(t, err, "managed-ci")
	if pulls := f.Pulls(); len(pulls) != 1 || pulls[0].Merged {
		t.Errorf("pull requests = %+v, want one, unmerged, since a failed check never lets it past", pulls)
	}
}

func TestUnsubscribe_MergesOnAPassedCheckWhileGitHubRefusesTheStatusRead(t *testing.T) {
	f, rec := waitingRemoval(t)
	f.ReportCheck("managed-ci", "success")
	f.RefuseCommitStatus()

	_, err := remove(client(f), rec)

	if err != nil {
		t.Fatalf("Remove once the check passed with the status read refused: %v", err)
	}
	if pulls := f.Pulls(); len(pulls) != 1 || !pulls[0].Merged {
		t.Errorf("pull requests = %+v, want the one removal merged", pulls)
	}
}

func TestObserver_ReadsTheRemovalUnderWayWhileItsCheckRunsAndGitHubRefusesTheStatusRead(t *testing.T) {
	f, rec := waitingRemoval(t)
	f.RunCheck("managed-ci", "in_progress")
	f.RefuseCommitStatus()

	obs := observeRemoval(t, f, rec, true)

	if obs.State != contract.Present || obs.Removing == nil {
		t.Errorf("observe while the check runs and the status read answers 403 = %s with removing %+v (%s), want present with removing", obs.State, obs.Removing, obs.StatusDetail)
	}
}
