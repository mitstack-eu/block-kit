package observe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

// committed serves the commits of every repository, newest first, over owned's topic search.
type committed struct {
	*owned
	commits []observe.GitHubCommitPaths
}

func (f committed) Commits(context.Context, contract.Desired, string) ([]observe.GitHubCommitPaths, error) {
	return f.commits, f.err
}

func TestGitHubCommit_FindsTheNewestTrailerCommitOfTheSubscription(t *testing.T) {
	trailer := "\n\n" + observe.GitHubSubscriptionTrailer("sub-1")
	boom := errors.New("github: rate limited")
	for _, tc := range []struct {
		label   string
		commits []observe.GitHubCommitPaths
		err     error
		state   contract.State
	}{
		{"two commits carry the trailer", []observe.GitHubCommitPaths{{SHA: "new1", Message: "Add" + trailer}, {SHA: "old0", Message: "Seed" + trailer}}, nil, contract.Present},
		{"no commit carries it", []observe.GitHubCommitPaths{{SHA: "other", Message: "Add\n\nMitstack-Subscription: sub-2"}}, nil, contract.Absent},
		{"the read failed", []observe.GitHubCommitPaths{{SHA: "new1", Message: "Add" + trailer}}, boom, contract.Absent},
	} {
		t.Run(tc.label, func(t *testing.T) {
			held := &owned{err: tc.err}
			held.held = map[string][]string{observe.GitHubSubscriptionTopic("sub-1"): {"acme/app"}}
			ref := refFor("github.commit", nil)
			ref.Unsubscribing = true
			got, err := observe.GitHubCommit{API: struct {
				observe.GitHubCommitAPI
				observe.GitHubTagAPI
			}{GitHubTagAPI: committed{owned: held, commits: tc.commits}}}.ObserveResource(context.Background(), ref)
			switch {
			case tc.err != nil && (!errors.Is(err, tc.err) || got.State == contract.Absent):
				t.Errorf("got %+v, %v; want the read's error, never absent", got, err)
			case tc.err == nil && (err != nil || got.State != tc.state):
				t.Errorf("got %+v, %v; want %s", got, err, tc.state)
			case tc.state == contract.Present && (got.Identity["repo"] != "acme/app" || got.Identity["sha"] != "new1"):
				t.Errorf("identity %v; want acme/app at the newest commit new1", got.Identity)
			}
		})
	}
}
