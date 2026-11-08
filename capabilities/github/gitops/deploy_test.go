package gitops_test

// Deploy commits a block's whole desired state once and waits for Argo CD's verdict on its own commit, the last one when nothing changed, or on a later revision holding its files.

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/gitops"
	"github.com/mitstack-eu/block-kit/capabilities/github/gitops/gitopstest"
)

// failingNotes answers a failed sync for any revision, since gitopstest seeds a verdict by a revision a test cannot know before Deploy commits.
type failingNotes struct{}

func (failingNotes) Latest(_ context.Context, app, revision string) (gitops.Notification, bool, error) {
	return gitops.Notification{App: app, Revision: revision, Phase: "Failed", Health: "Degraded", Message: "one or more objects failed to apply"}, true, nil
}

func alphaDeploy() gitops.BlockDeploy {
	return gitops.BlockDeploy{App: "app-alpha", Files: renderApp("app-alpha", "k3s-fsn1").Files, Message: "deploy app-alpha", Secrets: seededSecrets, Poll: time.Millisecond}
}

func TestDeploy(t *testing.T) {
	cases := []struct {
		name      string
		notes     func(repo *gitopstest.Repository) gitops.Notifications
		deploys   int
		in        func() gitops.BlockDeploy
		wantErr   error
		wantVerd  gitops.Verdict
		wantCount int
		check     func(t *testing.T, repo *gitopstest.Repository, first, last gitops.Deployed)
	}{
		{"it commits every file once under the message and answers the verdict on that revision", succeeding, 1, alphaDeploy, nil, gitops.VerdictSucceeded, 1,
			func(t *testing.T, repo *gitopstest.Repository, _, got gitops.Deployed) {
				c := repo.Commits()[0]
				if !got.Changed || got.Revision != c.Revision || got.Outcome.Notification.Revision != got.Revision {
					t.Errorf("Deployed = %+v, want changed on revision %q with its notification", got, c.Revision)
				}
				if !reflect.DeepEqual(c.Changes, alphaDeploy().Files) || c.Message != "deploy app-alpha" {
					t.Errorf("commit = %+v, want every file under the message %q", c, "deploy app-alpha")
				}
			}},
		{"an unchanged desired state lands nothing and waits on its own commit", succeeding, 2, alphaDeploy, nil, gitops.VerdictSucceeded, 1,
			func(t *testing.T, _ *gitopstest.Repository, first, again gitops.Deployed) {
				if again.Changed || again.Revision != first.Revision {
					t.Errorf("second Deployed changed, revision = %v, %q; want false and its own commit %q", again.Changed, again.Revision, first.Revision)
				}
			}},
		{"a failed verdict comes back as the outcome", func(*gitopstest.Repository) gitops.Notifications { return failingNotes{} }, 1, alphaDeploy, nil, gitops.VerdictFailed, 1,
			func(t *testing.T, _ *gitopstest.Repository, _, got gitops.Deployed) {
				if got.Outcome.Notification.Phase != "Failed" {
					t.Errorf("outcome notification = %+v, want the failed sync Argo CD reported", got.Outcome.Notification)
				}
			}},
		{"a file carrying a secret is refused and nothing is committed", succeeding, 1, func() gitops.BlockDeploy {
			in := alphaDeploy()
			in.Files["apps/app-alpha/leak.yaml"] = []byte("token: " + githubToken + "\n")
			return in
		}, gitops.ErrSecretInManifest, "", 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := gitopstest.NewRepository()
			notes := tc.notes(repo)
			var first, last gitops.Deployed
			var err error

			for i := range tc.deploys {
				last, err = gitops.Deploy(context.Background(), repo, notes, tc.in())
				if i == 0 {
					first = last
				}
			}

			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) || last.Outcome.Verdict != tc.wantVerd || len(repo.Commits()) != tc.wantCount {
				t.Fatalf("Deploy = %+v, %v with %d commits; want verdict %q, error %v, %d commits", last, err, len(repo.Commits()), tc.wantVerd, tc.wantErr, tc.wantCount)
			}
			if tc.check != nil {
				tc.check(t, repo, first, last)
			}
		})
	}
}

// argoAtHead stands in for Argo CD, which reports only the newest revision it synced, here a healthy head, and lands a sibling block's commit on its first read when one is armed.
type argoAtHead struct {
	repo    *gitopstest.Repository
	sibling map[string][]byte
}

func (a *argoAtHead) Latest(ctx context.Context, app, revision string) (gitops.Notification, bool, error) {
	if a.sibling != nil {
		head, err := a.repo.Head(ctx)
		if err != nil {
			return gitops.Notification{}, false, err
		}
		if _, err := a.repo.Commit(ctx, head, a.sibling, "app-beta: deploy"); err != nil {
			return gitops.Notification{}, false, err
		}
		a.sibling = nil
	}
	head, err := a.repo.Head(ctx)
	if err != nil || (revision != "" && revision != head) {
		return gitops.Notification{}, false, err
	}
	return gitops.Notification{App: app, Revision: head, Phase: "Succeeded", Health: "Healthy"}, true, nil
}

func betaFiles() map[string][]byte {
	return map[string][]byte{betaPath: []byte("beta\n")}
}

func betaDeploy() gitops.BlockDeploy {
	return gitops.BlockDeploy{App: "app-alpha", Files: betaFiles(), Message: "deploy app-beta", Poll: time.Millisecond}
}

// deployBriefly bounds a Deploy, so one held on a revision Argo CD never reports ends in DeadlineExceeded.
func deployBriefly(t *testing.T, repo *gitopstest.Repository, notes gitops.Notifications, b gitops.BlockDeploy) gitops.Deployed {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, err := gitops.Deploy(ctx, repo, notes, b)
	if err != nil || got.Outcome.Verdict != gitops.VerdictSucceeded {
		t.Fatalf("Deploy(%s) = %+v, %v; want it settled on Argo CD's newest revision %v", b.Message, got, err, repo.Commits())
	}
	return got
}

func TestDeploy_SharedRepository(t *testing.T) {
	t.Run("a sibling's commit landing after the block's own leaves it waiting on its own, and it settles on Argo CD's newer revision", func(t *testing.T) {
		repo := gitopstest.NewRepository()
		argo := &argoAtHead{repo: repo, sibling: betaFiles()}

		got := deployBriefly(t, repo, argo, alphaDeploy())

		commits := repo.Commits()
		if len(commits) != 2 || got.Revision != commits[0].Revision || got.Outcome.Notification.Revision != commits[1].Revision {
			t.Errorf("Deployed on %q settled at %q, commits %+v; want its own commit first and the sibling's after it as the settled revision", got.Revision, got.Outcome.Notification.Revision, commits)
		}
	})
	t.Run("files already at head, where head is a sibling's commit, record the block's own last commit and settle", func(t *testing.T) {
		repo := gitopstest.NewRepository()
		argo := &argoAtHead{repo: repo}
		first := deployBriefly(t, repo, argo, alphaDeploy())
		sibling, err := repo.Commit(context.Background(), first.Revision, betaFiles(), "deploy app-beta")
		if err != nil {
			t.Fatal(err)
		}

		again := deployBriefly(t, repo, argo, alphaDeploy())

		if again.Changed || again.Revision != first.Revision || again.Outcome.Notification.Revision != sibling {
			t.Errorf("second Deployed = %+v; want unchanged on its own commit %q, settled at the sibling's head %q", again, first.Revision, sibling)
		}
	})
	t.Run("two blocks committing at one moment each record their own commit, and neither is held on the other's", func(t *testing.T) {
		repo := gitopstest.NewRepository()
		argo := &argoAtHead{repo: repo}
		repo.MoveTip(1, betaFiles())

		alpha := deployBriefly(t, repo, argo, alphaDeploy())
		beta := deployBriefly(t, repo, argo, betaDeploy())

		commits := repo.Commits()
		if len(commits) != 2 || beta.Revision != commits[0].Revision || alpha.Revision != commits[1].Revision {
			t.Errorf("alpha on %q, beta on %q, commits %+v; want beta's commit first and alpha's landed on it, each recorded by its own block", alpha.Revision, beta.Revision, commits)
		}
	})
}

func succeeding(repo *gitopstest.Repository) gitops.Notifications {
	notes := gitopstest.NewNotifications()
	notes.SucceedCommits("app-alpha", repo)
	return notes
}
