package observe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

func TestGitHubCommit_FindsTheNewestTrailerCommitOfTheSubscription(t *testing.T) {
	trailer := "\n\n" + observe.GitHubSubscriptionTrailer("sub-1")
	boom := errors.New("github: rate limited")
	two := []observe.GitHubCommitPaths{{SHA: "new1", Message: "Add" + trailer}, {SHA: "old0", Message: "Seed" + trailer}}
	for label, tc := range map[string]struct {
		commits []observe.GitHubCommitPaths
		err     error
		state   contract.State
		fails   bool
	}{
		"two commits carry the trailer": {two, nil, contract.Present, false},
		"no commit carries it":          {[]observe.GitHubCommitPaths{{SHA: "x", Message: "Add\n\nMitstack-Subscription: sub-2"}}, nil, contract.Absent, false},
		"a trailer commit has no sha":   {[]observe.GitHubCommitPaths{{Message: "Add" + trailer}}, nil, contract.Absent, true},
		"the read failed":               {two, boom, contract.Absent, false},
	} {
		t.Run(label, func(t *testing.T) {
			api := &owned{err: tc.err, commits: tc.commits, held: map[string][]string{observe.GitHubSubscriptionTopic("sub-1"): {"acme/app"}}}
			ref := refFor("github.commit", nil)
			ref.Unsubscribing = true
			got, err := observe.GitHubCommit{API: api}.ObserveResource(context.Background(), ref)
			switch {
			case tc.fails && (err == nil || got.State == contract.Absent):
				t.Errorf("got %+v, %v; want an error naming the commit with no sha, never absent", got, err)
			case tc.err != nil && (!errors.Is(err, tc.err) || got.State == contract.Absent):
				t.Errorf("got %+v, %v; want the read's error, never absent", got, err)
			case tc.err == nil && !tc.fails && (err != nil || got.State != tc.state || (tc.state == contract.Present && (got.Identity["repo"] != "acme/app" || got.Identity["sha"] != "new1"))):
				t.Errorf("got %+v, %v; want %s at acme/app, new1", got, err, tc.state)
			}
		})
	}
}
