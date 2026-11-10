package gitopstest_test

// Repository, Notifications and NodeReports behave as the gitops.Repository, gitops.Notifications and observe.NodeReportsAPI a block author tests against, as their doc comments say.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/gitops"
	"github.com/mitstack-eu/block-kit/capabilities/github/gitops/gitopstest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

var (
	_ gitops.Repository      = (*gitopstest.Repository)(nil)
	_ gitops.Notifications   = (*gitopstest.Notifications)(nil)
	_ observe.NodeReportsAPI = (*gitopstest.NodeReports)(nil)
)

func TestRepository_AnEmptyRepositoryHasHeadEmptyAndHoldsNoFile(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()

	head, err := repo.Head(ctx)
	if err != nil || head != "" {
		t.Fatalf("Head = %q, %v, want \"\", nil for an empty repository", head, err)
	}
	content, ok, err := repo.ReadFile(ctx, "", "apps/app-alpha/namespace.yaml")
	if err != nil || ok || content != nil {
		t.Errorf("ReadFile at revision \"\" = %q, %v, %v, want nil, false, nil", content, ok, err)
	}
	if got := repo.Commits(); len(got) != 0 {
		t.Errorf("Commits = %+v, want none", got)
	}
}

func TestRepository_ACommitOnTheTipLandsAndMovesHead(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()

	rev, err := repo.Commit(ctx, "", map[string][]byte{"a.yaml": []byte("a: 1\n")}, "add a")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	head, err := repo.Head(ctx)
	if err != nil || head != rev {
		t.Errorf("Head = %q, %v, want the new revision %q", head, err, rev)
	}
	content, ok, err := repo.ReadFile(ctx, rev, "a.yaml")
	if err != nil || !ok || string(content) != "a: 1\n" {
		t.Errorf("ReadFile(a.yaml) = %q, %v, %v, want the committed content", content, ok, err)
	}
	_, ok, err = repo.ReadFile(ctx, rev, "b.yaml")
	if err != nil || ok {
		t.Errorf("ReadFile(b.yaml) = %v, %v, want false, nil for a path never written", ok, err)
	}
}

func TestRepository_ACommitOnAParentThatIsNotTheTipFailsWithErrStaleBaseAndLandsNothing(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	first, err := repo.Commit(ctx, "", map[string][]byte{"a.yaml": []byte("a: 1\n")}, "add a")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	_, err = repo.Commit(ctx, "", map[string][]byte{"b.yaml": []byte("b: 1\n")}, "add b on a stale base")
	if !errors.Is(err, gitops.ErrStaleBase) {
		t.Fatalf("Commit on parent \"\" with tip %q: err = %v, want gitops.ErrStaleBase", first, err)
	}
	if got := len(repo.Commits()); got != 1 {
		t.Errorf("Commits = %d, want 1: the stale commit must land nothing", got)
	}
	if head, _ := repo.Head(ctx); head != first {
		t.Errorf("Head = %q, want %q unchanged", head, first)
	}
}

func TestRepository_ANilChangeDeletesItsPathAndOtherPathsCarryOver(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	first, err := repo.Commit(ctx, "", map[string][]byte{"a.yaml": []byte("a\n"), "b.yaml": []byte("b\n")}, "add a and b")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	second, err := repo.Commit(ctx, first, map[string][]byte{"a.yaml": nil}, "delete a")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if _, ok, err := repo.ReadFile(ctx, second, "a.yaml"); err != nil || ok {
		t.Errorf("ReadFile(a.yaml) at the deleting commit = %v, %v, want false, nil", ok, err)
	}
	if content, ok, err := repo.ReadFile(ctx, second, "b.yaml"); err != nil || !ok || string(content) != "b\n" {
		t.Errorf("ReadFile(b.yaml) at the deleting commit = %q, %v, %v, want it carried over", content, ok, err)
	}
	if content, ok, err := repo.ReadFile(ctx, first, "a.yaml"); err != nil || !ok || string(content) != "a\n" {
		t.Errorf("ReadFile(a.yaml) at the first commit = %q, %v, %v, want history kept", content, ok, err)
	}
}

func TestRepository_ReadFileAndChangedRefuseARevisionTheRepositoryNeverHeld(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()

	if _, _, err := repo.ReadFile(ctx, "no-such-revision", "a.yaml"); err == nil {
		t.Error("ReadFile at an unknown revision returned no error")
	}
	if _, _, err := repo.Changed(ctx, "no-such-revision"); err == nil {
		t.Error("Changed of an unknown revision returned no error")
	}
}

func TestRepository_ChangedReportsTheParentAndTheSortedPathsACommitTouched(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	first, err := repo.Commit(ctx, "", map[string][]byte{"z.yaml": []byte("z\n"), "a.yaml": []byte("a\n")}, "add")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	second, err := repo.Commit(ctx, first, map[string][]byte{"z.yaml": nil, "m.yaml": []byte("m\n")}, "change")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	parent, paths, err := repo.Changed(ctx, first)
	if err != nil || parent != "" || !reflect.DeepEqual(paths, []string{"a.yaml", "z.yaml"}) {
		t.Errorf("Changed(first) = %q, %q, %v, want \"\", [a.yaml z.yaml], nil", parent, paths, err)
	}
	parent, paths, err = repo.Changed(ctx, second)
	if err != nil || parent != first || !reflect.DeepEqual(paths, []string{"m.yaml", "z.yaml"}) {
		t.Errorf("Changed(second) = %q, %q, %v, want %q, [m.yaml z.yaml], nil", parent, paths, err, first)
	}
}

func TestRepository_CommitsRecordsEveryLandedCommitInOrder(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	first, _ := repo.Commit(ctx, "", map[string][]byte{"a.yaml": []byte("a\n")}, "add a")
	second, _ := repo.Commit(ctx, first, map[string][]byte{"a.yaml": nil}, "delete a")

	want := []gitopstest.Commit{
		{Revision: first, Parent: "", Message: "add a", Changes: map[string][]byte{"a.yaml": []byte("a\n")}},
		{Revision: second, Parent: first, Message: "delete a", Changes: map[string][]byte{"a.yaml": nil}},
	}
	if got := repo.Commits(); !reflect.DeepEqual(got, want) {
		t.Errorf("Commits = %+v, want %+v", got, want)
	}
	if first == second {
		t.Errorf("two commits share revision %q", first)
	}
}

func TestRepository_ACommitKeepsItsOwnCopyOfTheContentItWasHanded(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	content := []byte("a: 1\n")
	changes := map[string][]byte{"a.yaml": content}
	rev, err := repo.Commit(ctx, "", changes, "add a")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	content[0] = 'X'
	changes["b.yaml"] = []byte("b\n")

	got, ok, err := repo.ReadFile(ctx, rev, "a.yaml")
	if err != nil || !ok || string(got) != "a: 1\n" {
		t.Errorf("ReadFile(a.yaml) = %q, %v, %v after the caller changed its bytes, want the committed %q", got, ok, err, "a: 1\n")
	}
	if _, ok, _ := repo.ReadFile(ctx, rev, "b.yaml"); ok {
		t.Error("a path the caller added to its map after the commit appeared in the repository")
	}
}

func TestRepository_MoveTipMakesEachOfTheNextNCommitsFindAnotherWritersCommitFirst(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	foreign := map[string][]byte{"apps/app-beta/config.yaml": []byte("beta\n")}
	repo.MoveTip(2, foreign)

	for i := 1; i <= 2; i++ {
		head, _ := repo.Head(ctx)
		_, err := repo.Commit(ctx, head, map[string][]byte{"a.yaml": []byte("a\n")}, "add a")
		if !errors.Is(err, gitops.ErrStaleBase) {
			t.Fatalf("commit %d after MoveTip(2): err = %v, want gitops.ErrStaleBase", i, err)
		}
	}
	commits := repo.Commits()
	if len(commits) != 2 {
		t.Fatalf("Commits = %d after two stale commits, want the 2 foreign commits that moved the tip", len(commits))
	}
	for i, c := range commits {
		if !reflect.DeepEqual(c.Changes, foreign) {
			t.Errorf("foreign commit %d changes = %q, want the changes MoveTip was handed", i, c.Changes)
		}
	}

	head, _ := repo.Head(ctx)
	if head != commits[1].Revision {
		t.Errorf("Head = %q, want the second foreign commit %q", head, commits[1].Revision)
	}
	rev, err := repo.Commit(ctx, head, map[string][]byte{"a.yaml": []byte("a\n")}, "add a")
	if err != nil {
		t.Fatalf("the third commit after MoveTip(2): %v, want it to land", err)
	}
	if content, ok, _ := repo.ReadFile(ctx, rev, "apps/app-beta/config.yaml"); !ok || string(content) != "beta\n" {
		t.Errorf("the foreign file at the landed commit = %q, %v, want it kept", content, ok)
	}
}

func adrNotification(revision string) gitops.Notification {
	return gitops.Notification{
		App: "app-alpha", Project: "app-alpha", Sync: "Synced", Revision: revision,
		Health: "Healthy", Phase: "Succeeded", Message: "successfully synced (all tasks run)",
	}
}

func TestNotifications_LatestAnswersWhatSetSeededAndNothingElse(t *testing.T) {
	ctx := context.Background()
	notes := gitopstest.NewNotifications()
	seeded := adrNotification("rev-1")
	notes.Set(seeded)

	got, ok, err := notes.Latest(ctx, "app-alpha", "rev-1")

	if err != nil || !ok || !reflect.DeepEqual(got, seeded) {
		t.Errorf("Latest(app-alpha, rev-1) = %+v, %v, %v, want the seeded notification", got, ok, err)
	}
	for _, q := range [][2]string{{"app-alpha", "rev-2"}, {"app-beta", "rev-1"}} {
		if _, ok, err := notes.Latest(ctx, q[0], q[1]); err != nil || ok {
			t.Errorf("Latest(%s, %s) = %v, %v, want none for what was never seeded", q[0], q[1], ok, err)
		}
	}
}

func TestNotifications_SequenceAnswersEachInTurnThenRepeatsTheLastAndAZeroNotificationIsNone(t *testing.T) {
	ctx := context.Background()
	notes := gitopstest.NewNotifications()
	running := gitops.Notification{App: "app-alpha", Revision: "rev-1", Phase: "Running", Health: "Progressing"}
	done := adrNotification("rev-1")
	notes.Sequence("app-alpha", "rev-1", gitops.Notification{}, running, done)

	if _, ok, err := notes.Latest(ctx, "app-alpha", "rev-1"); err != nil || ok {
		t.Errorf("read 1 = %v, %v, want none: the zero notification answers none", ok, err)
	}
	if got, ok, _ := notes.Latest(ctx, "app-alpha", "rev-1"); !ok || !reflect.DeepEqual(got, running) {
		t.Errorf("read 2 = %+v, %v, want the running notification", got, ok)
	}
	for i := 3; i <= 4; i++ {
		if got, ok, _ := notes.Latest(ctx, "app-alpha", "rev-1"); !ok || !reflect.DeepEqual(got, done) {
			t.Errorf("read %d = %+v, %v, want the last notification, repeated", i, got, ok)
		}
	}
	if got := notes.Reads("app-alpha", "rev-1"); got != 4 {
		t.Errorf("Reads = %d, want 4", got)
	}
}

func TestNotifications_SucceedCommitsAnswersSucceededAndHealthyForEveryRevisionTheRepositoryHolds(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	notes := gitopstest.NewNotifications()
	first, _ := repo.Commit(ctx, "", map[string][]byte{"a.yaml": []byte("a\n")}, "add a")
	notes.SucceedCommits("app-alpha", repo)
	second, _ := repo.Commit(ctx, first, map[string][]byte{"a.yaml": nil}, "delete a")

	for _, rev := range []string{first, second} {
		got, ok, err := notes.Latest(ctx, "app-alpha", rev)
		if err != nil || !ok {
			t.Errorf("Latest(app-alpha, %s) = %v, %v, want a notification for a revision the repository holds", rev, ok, err)
			continue
		}
		if got.App != "app-alpha" || got.Revision != rev || got.Phase != "Succeeded" || got.Health != "Healthy" {
			t.Errorf("Latest(app-alpha, %s) = %+v, want app-alpha at that revision, phase Succeeded, health Healthy", rev, got)
		}
	}
	if _, ok, _ := notes.Latest(ctx, "app-alpha", "never-committed"); ok {
		t.Error("SucceedCommits answered for a revision the repository never held")
	}
	if _, ok, _ := notes.Latest(ctx, "app-beta", first); ok {
		t.Error("SucceedCommits for app-alpha answered for app-beta")
	}
}

func TestNotifications_LatestForRevisionEmptyAnswersTheNotificationMostRecentlySetForTheApp(t *testing.T) {
	ctx := context.Background()
	notes := gitopstest.NewNotifications()

	if _, ok, err := notes.Latest(ctx, "app-alpha", ""); err != nil || ok {
		t.Errorf("Latest(app-alpha, \"\") with nothing set = %v, %v, want none", ok, err)
	}

	older := adrNotification("rev-2")
	newer := gitops.Notification{App: "app-alpha", Revision: "rev-1", Phase: "Running", Health: "Progressing"}
	notes.Set(older)
	notes.Set(gitops.Notification{App: "app-beta", Revision: "rev-9", Phase: "Succeeded", Health: "Healthy"})
	notes.Set(newer)

	got, ok, err := notes.Latest(ctx, "app-alpha", "")
	if err != nil || !ok || !reflect.DeepEqual(got, newer) {
		t.Errorf("Latest(app-alpha, \"\") = %+v, %v, %v, want the notification set last for app-alpha, whatever its revision", got, ok, err)
	}
	if got, ok, _ := notes.Latest(ctx, "app-alpha", "rev-2"); !ok || !reflect.DeepEqual(got, older) {
		t.Errorf("Latest(app-alpha, rev-2) = %+v, %v, want the exact-revision answer kept", got, ok)
	}
	if got := notes.Reads("app-alpha", ""); got != 2 {
		t.Errorf("Reads(app-alpha, \"\") = %d, want 2", got)
	}
}

func TestNotifications_SucceedCommitsAnswersRevisionEmptyWithTheRepositorysCurrentHead(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	notes := gitopstest.NewNotifications()
	notes.SucceedCommits("app-alpha", repo)

	if _, ok, err := notes.Latest(ctx, "app-alpha", ""); err != nil || ok {
		t.Errorf("Latest(app-alpha, \"\") on an empty repository = %v, %v, want none", ok, err)
	}

	first, _ := repo.Commit(ctx, "", map[string][]byte{"a.yaml": []byte("a\n")}, "add a")
	second, _ := repo.Commit(ctx, first, map[string][]byte{"b.yaml": []byte("b\n")}, "add b")

	got, ok, err := notes.Latest(ctx, "app-alpha", "")
	if err != nil || !ok {
		t.Fatalf("Latest(app-alpha, \"\") = %v, %v, want a notification", ok, err)
	}
	if got.App != "app-alpha" || got.Revision != second || got.Phase != "Succeeded" || got.Health != "Healthy" {
		t.Errorf("Latest(app-alpha, \"\") = %+v, want app-alpha at head %s, phase Succeeded, health Healthy", got, second)
	}
	if _, ok, _ := notes.Latest(ctx, "app-beta", ""); ok {
		t.Error("SucceedCommits for app-alpha answered Latest(app-beta, \"\")")
	}
}

func TestNotifications_ANotificationSetForTheAppAnswersRevisionEmptyOverTheAutoAnswer(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	notes := gitopstest.NewNotifications()
	notes.SucceedCommits("app-alpha", repo)
	first, _ := repo.Commit(ctx, "", map[string][]byte{"a.yaml": []byte("a\n")}, "add a")
	if _, err := repo.Commit(ctx, first, map[string][]byte{"b.yaml": []byte("b\n")}, "add b"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	pinned := gitops.Notification{App: "app-alpha", Revision: first, Phase: "Running", Health: "Progressing"}
	notes.Set(pinned)

	if got, ok, _ := notes.Latest(ctx, "app-alpha", ""); !ok || !reflect.DeepEqual(got, pinned) {
		t.Errorf("Latest(app-alpha, \"\") = %+v, %v, want the notification set for the app, not the auto answer for head", got, ok)
	}
}

func TestNotifications_SetOverridesTheAutoAnswerForItsRevision(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	notes := gitopstest.NewNotifications()
	notes.SucceedCommits("app-alpha", repo)
	rev, _ := repo.Commit(ctx, "", map[string][]byte{"a.yaml": []byte("a\n")}, "add a")
	failed := gitops.Notification{App: "app-alpha", Revision: rev, Phase: "Failed", Health: "Degraded", Message: "ImagePullBackOff"}
	notes.Set(failed)

	if got, ok, _ := notes.Latest(ctx, "app-alpha", rev); !ok || !reflect.DeepEqual(got, failed) {
		t.Errorf("Latest = %+v, %v, want the seeded failure over the auto answer", got, ok)
	}
}

func TestNotifications_FailWithMakesEveryReadReturnTheError(t *testing.T) {
	ctx := context.Background()
	notes := gitopstest.NewNotifications()
	notes.Set(adrNotification("rev-1"))
	boom := errors.New("notification store unreachable")
	notes.FailWith(boom)

	if _, _, err := notes.Latest(ctx, "app-alpha", "rev-1"); !errors.Is(err, boom) {
		t.Errorf("Latest err = %v, want the FailWith error", err)
	}
}

func TestNodeReports_AppliesHeadShowsEachObjectHeadsFilesDeclareSyncedAndHealthyButThoseSet(t *testing.T) {
	ctx := context.Background()
	repo := gitopstest.NewRepository()
	head, _ := repo.Commit(ctx, "", map[string][]byte{"ns.yaml": []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: app-alpha\n"),
		"sa.json": []byte(`{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": {"name": "runtime", "namespace": "app-alpha"}}`)}, "deploy")
	account := gitops.Object{Kind: "ServiceAccount", Namespace: "app-alpha", Name: "runtime"}
	reports := gitopstest.NewNodeReports()
	reports.AppliesHead("sub-cluster", "app-alpha", repo, gitopstest.Object{Object: account, Sync: "OutOfSync", Health: "Missing"})
	d := contract.Desired{VerbAt: time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)}

	got, why, err := observe.NodeReported{API: reports}.ArgoCDApplication(ctx, d, "sub-cluster", "app-alpha")

	want := observe.Application{Name: "app-alpha", Sync: "Synced", Revision: head, Objects: []observe.AppliedObject{
		{Kind: "Namespace", Name: "app-alpha", Sync: "Synced", Health: observe.Health{Status: "Healthy"}},
		{Kind: "ServiceAccount", Namespace: "app-alpha", Name: "runtime", Sync: "OutOfSync", Health: observe.Health{Status: "Missing"}},
	}}
	if err != nil || why != "" || !reflect.DeepEqual(got, want) {
		t.Errorf("ArgoCDApplication = %+v, %q, %v; want %+v", got, why, err, want)
	}
}

func TestNodeReports_ReportAnswersOnlyItsClusterAndLeavesOutWhatTheApplicationLeavesOut(t *testing.T) {
	reports := gitopstest.NewNodeReports()
	madeAt := time.Date(2026, 11, 9, 9, 1, 0, 0, time.UTC)
	reports.Report("sub-cluster", madeAt, gitopstest.Application{Name: "app-alpha", RevisionLeftOut: true})

	got, why, err := observe.NodeReported{API: reports}.ArgoCDApplication(context.Background(), contract.Desired{VerbAt: madeAt.Add(-time.Minute)}, "sub-cluster", "app-alpha")
	other, _ := reports.NodeReports(context.Background(), contract.Desired{}, "sub-other")

	if err != nil || why != "" || !got.RevisionUnread || got.Objects != nil || len(other) != 0 {
		t.Errorf("ArgoCDApplication = %+v, %q, %v and %d reports of another cluster; want no revision, no list and none", got, why, err, len(other))
	}
}

func TestNodeReports_ReportWritesEachApplicationAsTalosClusterKitsNodeReportDoes(t *testing.T) {
	reports := gitopstest.NewNodeReports()
	reports.Report("sub-cluster", time.Date(2026, 11, 9, 9, 1, 0, 0, time.UTC),
		gitopstest.Application{Name: "app-alpha", Revision: "9b2c4e7a1f3d5c8e0a6b4d2f9e7c5a3b1d8f6e4c", Objects: []gitopstest.Object{
			gitopstest.Healthy(gitops.Object{Kind: "Namespace", Name: "app-alpha"}),
			{Object: gitops.Object{Kind: "ServiceAccount", Namespace: "app-alpha", Name: "runtime"}, Sync: "Synced"},
			{Object: gitops.Object{Group: "postgresql.cnpg.io", Kind: "Cluster", Namespace: "app-pg", Name: "shared"}, Sync: "OutOfSync", Health: "Degraded", Message: "the storage class local-path is missing"},
			{Object: gitops.Object{Group: "apps", Kind: "Deployment", Namespace: "app-alpha", Name: "web"}, Sync: "Synced", Health: "Progressing"},
			{Object: gitops.Object{Kind: "Secret", Namespace: "app-alpha", Name: "env"}, Sync: "Synced", HealthLeftOut: true},
		}},
		gitopstest.Application{Name: "app-beta", Objects: []gitopstest.Object{}},
		gitopstest.Application{Name: "app-gamma", Revision: "4e1d7a0c3b6f9e2d5a8c1b4e7f0a3d6c9b2e5f8a"},
		gitopstest.Application{Name: "app-delta", RevisionLeftOut: true})
	want := `{"madeAt": "2026-11-09T09:01:00Z", "argocd": {"applications": [
		{"name": "app-alpha", "health": "Healthy", "sync": "Synced", "revision": "9b2c4e7a1f3d5c8e0a6b4d2f9e7c5a3b1d8f6e4c", "resources": [
			{"group": "", "kind": "Namespace", "namespace": null, "name": "app-alpha", "sync": "Synced", "health": "Healthy"},
			{"group": "", "kind": "ServiceAccount", "namespace": "app-alpha", "name": "runtime", "sync": "Synced", "health": null},
			{"group": "postgresql.cnpg.io", "kind": "Cluster", "namespace": "app-pg", "name": "shared", "sync": "OutOfSync", "health": "Degraded", "message": "the storage class local-path is missing"},
			{"group": "apps", "kind": "Deployment", "namespace": "app-alpha", "name": "web", "sync": "Synced", "health": "Progressing", "message": null},
			{"group": "", "kind": "Secret", "namespace": "app-alpha", "name": "env", "sync": "Synced"}]},
		{"name": "app-beta", "health": "Healthy", "sync": "Synced", "revision": null, "resources": []},
		{"name": "app-gamma", "health": "Healthy", "sync": "Synced", "revision": "4e1d7a0c3b6f9e2d5a8c1b4e7f0a3d6c9b2e5f8a"},
		{"name": "app-delta", "health": "Healthy", "sync": "Synced"}]}}`

	got, err := reports.NodeReports(context.Background(), contract.Desired{}, "sub-cluster")

	var body, wantBody any
	if err != nil || len(got) != 1 || json.Unmarshal(got[0].Body, &body) != nil || json.Unmarshal([]byte(want), &wantBody) != nil || !reflect.DeepEqual(body, wantBody) {
		bodies := make([]string, 0, len(got))
		for _, r := range got {
			bodies = append(bodies, string(r.Body))
		}
		t.Errorf("NodeReports = %q, %v; want one report whose body is %s", bodies, err, want)
	}
}

func TestNodeReports_FailWithMakesEveryReadReturnTheError(t *testing.T) {
	reports := gitopstest.NewNodeReports()
	boom := errors.New("platform: 503")
	reports.FailWith(boom)

	_, err := reports.NodeReports(context.Background(), contract.Desired{}, "sub-cluster")

	if !errors.Is(err, boom) {
		t.Errorf("NodeReports error = %v, want %v", err, boom)
	}
}

const (
	webManifest     = `{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "web", "namespace": "app-alpha"}}`
	accountManifest = "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: runtime\n  namespace: app-alpha\n"
	siblingManifest = `{"apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": "migrate", "namespace": "app-alpha"}}`
)

var (
	web            = gitops.Object{Group: "apps", Kind: "Deployment", Namespace: "app-alpha", Name: "web"}
	runtimeAccount = gitops.Object{Kind: "ServiceAccount", Namespace: "app-alpha", Name: "runtime"}
	siblingJob     = gitops.Object{Group: "batch", Kind: "Job", Namespace: "app-alpha", Name: "migrate"}
	appPaths       = []string{"app/deployment.json", "app/account.yaml"}
	verbAt         = time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
)

// webThenRemoved commits the block's Deployment and ServiceAccount, then removes the Deployment beside a sibling's Job, and returns both revisions.
func webThenRemoved(t *testing.T) (repo *gitopstest.Repository, deployed, removed string) {
	t.Helper()
	ctx := context.Background()
	repo = gitopstest.NewRepository()
	deployed, err := repo.Commit(ctx, "", map[string][]byte{"app/deployment.json": []byte(webManifest), "app/account.yaml": []byte(accountManifest)}, "deploy web")
	if err != nil {
		t.Fatal(err)
	}
	removed, err = repo.Commit(ctx, deployed, map[string][]byte{"app/deployment.json": nil, "sibling/job.json": []byte(siblingManifest)}, "remove web")
	if err != nil {
		t.Fatal(err)
	}
	return repo, deployed, removed
}

func appliedAs(o gitops.Object, sync, health string) observe.AppliedObject {
	return observe.AppliedObject{Group: o.Group, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, Sync: sync, Health: observe.Health{Status: health}}
}

func TestNodeReports_AppliesShowsTheApplicationAtItsRevisionListingThePathsObjectsThenEachSetObjectNoFileDeclares(t *testing.T) {
	repo, deployed, removed := webThenRemoved(t)
	cases := []struct {
		name     string
		applied  gitopstest.Applied
		revision string
		objects  []observe.AppliedObject
	}{
		{"an empty Revision shows head, each object of the files Paths names Synced and Healthy", gitopstest.Applied{Paths: appPaths},
			removed, []observe.AppliedObject{appliedAs(runtimeAccount, "Synced", "Healthy")}},
		{"an earlier Revision lists the objects its files declare, in the order of Paths", gitopstest.Applied{Revision: deployed, Paths: appPaths},
			deployed, []observe.AppliedObject{appliedAs(web, "Synced", "Healthy"), appliedAs(runtimeAccount, "Synced", "Healthy")}},
		{"a set object no file declares follows the declared ones, as one Argo CD has yet to prune",
			gitopstest.Applied{Paths: appPaths, Set: []gitopstest.Object{{Object: web, Sync: "OutOfSync", Health: "Healthy"}}},
			removed, []observe.AppliedObject{appliedAs(runtimeAccount, "Synced", "Healthy"), appliedAs(web, "OutOfSync", "Healthy")}},
		{"a set object a file declares takes the state set gives it",
			gitopstest.Applied{Revision: deployed, Paths: appPaths, Set: []gitopstest.Object{{Object: runtimeAccount, Sync: "OutOfSync", Health: "Missing"}}},
			deployed, []observe.AppliedObject{appliedAs(web, "Synced", "Healthy"), appliedAs(runtimeAccount, "OutOfSync", "Missing")}},
		{"no Paths lists the objects of every file in path order, a sibling's among them", gitopstest.Applied{},
			removed, []observe.AppliedObject{appliedAs(runtimeAccount, "Synced", "Healthy"), appliedAs(siblingJob, "Synced", "Healthy")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reports := gitopstest.NewNodeReports()
			tc.applied.Name, tc.applied.Repo = "app-alpha", repo
			reports.Applies("sub-cluster", tc.applied)

			got, why, err := observe.NodeReported{API: reports}.ArgoCDApplication(context.Background(), contract.Desired{VerbAt: verbAt}, "sub-cluster", "app-alpha")

			want := observe.Application{Name: "app-alpha", Sync: "Synced", Revision: tc.revision, Objects: tc.objects}
			if err != nil || why != "" || !reflect.DeepEqual(got, want) {
				t.Errorf("ArgoCDApplication = %+v, %q, %v; want %+v", got, why, err, want)
			}
		})
	}
}

func TestApplied_ApplicationIsTheApplicationAppliesShowsForAReportReportAdds(t *testing.T) {
	repo, _, _ := webThenRemoved(t)
	applied := gitopstest.Applied{Name: "app-alpha", Repo: repo, Paths: appPaths, Set: []gitopstest.Object{{Object: web, Sync: "OutOfSync", Health: "Healthy"}}}
	shown, added := gitopstest.NewNodeReports(), gitopstest.NewNodeReports()
	shown.Applies("sub-cluster", applied)

	app, err := applied.Application()

	added.Report("sub-cluster", verbAt.Add(time.Minute), app)
	want, _ := shown.NodeReports(context.Background(), contract.Desired{VerbAt: verbAt}, "sub-cluster")
	got, _ := added.NodeReports(context.Background(), contract.Desired{}, "sub-cluster")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("a report of Application() = %q, %v; want the report Applies shows, %q", bodiesOf(got), err, bodiesOf(want))
	}
}

func TestNodeReports_AppliesShowsAnApplicationInPlaceOfTheOneOfItsNameAnEarlierCallShowed(t *testing.T) {
	repo, deployed, removed := webThenRemoved(t)
	reports := gitopstest.NewNodeReports()
	reports.AppliesHead("sub-cluster", "app-alpha", repo, gitopstest.Object{Object: runtimeAccount, Sync: "OutOfSync", Health: "Missing"})
	reports.AppliesHead("sub-cluster", "app-beta", repo)
	reports.Applies("sub-cluster", gitopstest.Applied{Name: "app-alpha", Repo: repo, Revision: deployed, Paths: appPaths})

	got, err := reports.NodeReports(context.Background(), contract.Desired{VerbAt: verbAt}, "sub-cluster")

	var body struct {
		ArgoCD struct {
			Applications []struct{ Name, Revision string }
		}
	}
	want := []struct{ Name, Revision string }{{"app-alpha", deployed}, {"app-beta", removed}}
	if err != nil || len(got) != 1 || json.Unmarshal(got[0].Body, &body) != nil || !reflect.DeepEqual(body.ArgoCD.Applications, want) {
		t.Errorf("NodeReports = %q, %v; want one report showing %+v", bodiesOf(got), err, want)
	}
}

func TestNodeReports_AReadFailsWhileAppliesNamesARevisionTheRepositoryNeverHeld(t *testing.T) {
	repo, _, _ := webThenRemoved(t)
	unknown := strings.Repeat("f", 40)
	reports := gitopstest.NewNodeReports()
	reports.Applies("sub-cluster", gitopstest.Applied{Name: "app-alpha", Repo: repo, Revision: unknown})

	_, err := reports.NodeReports(context.Background(), contract.Desired{}, "sub-cluster")

	if err == nil || !strings.Contains(err.Error(), unknown) {
		t.Errorf("NodeReports error = %v, want one naming the revision %s the repository never held", err, unknown)
	}
}

func bodiesOf(reports []observe.NodeReport) []string {
	bodies := make([]string, 0, len(reports))
	for _, r := range reports {
		bodies = append(bodies, string(r.Body))
	}
	return bodies
}

func TestNodeReports_AddToGivesEachReportTheApplicationsOfTheNewestReportItAnswersAndKeepsTheReportsOwnMembers(t *testing.T) {
	repo, _, removed := webThenRemoved(t)
	alpha := gitopstest.Applied{Name: "app-alpha", Repo: repo, Paths: appPaths}
	alphaJSON := fmt.Sprintf(`{"name": "app-alpha", "health": "Healthy", "sync": "Synced", "revision": %q, "resources": [
		{"group": "", "kind": "ServiceAccount", "namespace": "app-alpha", "name": "runtime", "sync": "Synced", "health": "Healthy"}]}`, removed)
	own := `"madeAt": "2026-11-09T09:00:30Z", "subscriptionResources": [{"namespace": "app-alpha", "kind": "configmap", "name": "env", "subscription": "sub-1"}]`
	cases := []struct {
		name       string
		arrange    func(r *gitopstest.NodeReports)
		body, want string
	}{
		{"it gets the view Applies shows a minute after the verb, newer than a report Report added", func(r *gitopstest.NodeReports) {
			r.Report("sub-cluster", verbAt.Add(-time.Hour), gitopstest.Application{Name: "app-earlier"})
			r.Applies("sub-cluster", alpha)
		}, "{" + own + "}", "{" + own + `, "argocd": {"applications": [` + alphaJSON + "]}}"},
		{"it gets a report Report added after that view", func(r *gitopstest.NodeReports) {
			r.Applies("sub-cluster", alpha)
			r.Report("sub-cluster", verbAt.Add(time.Hour), gitopstest.Application{Name: "app-later", Objects: []gitopstest.Object{}})
		}, "{" + own + "}", "{" + own + `, "argocd": {"applications": [{"name": "app-later", "health": "Healthy", "sync": "Synced", "revision": null, "resources": []}]}}`},
		{"a report carrying Applications of its own keeps them", func(r *gitopstest.NodeReports) { r.Applies("sub-cluster", alpha) },
			"{" + own + `, "argocd": {"applications": []}}`, "{" + own + `, "argocd": {"applications": []}}`},
		{"a report stays as it came while no report shows an Application", func(*gitopstest.NodeReports) {}, "{" + own + "}", "{" + own + "}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reports := gitopstest.NewNodeReports()
			tc.arrange(reports)
			received := verbAt.Add(2 * time.Minute)

			got, err := reports.AddTo(context.Background(), contract.Desired{VerbAt: verbAt}, "sub-cluster", []observe.NodeReport{{ReceivedAt: received, Body: json.RawMessage(tc.body)}})

			var body, want any
			if err != nil || len(got) != 1 || !got[0].ReceivedAt.Equal(received) || json.Unmarshal(got[0].Body, &body) != nil || json.Unmarshal([]byte(tc.want), &want) != nil || !reflect.DeepEqual(body, want) {
				t.Errorf("AddTo = %q, %v; want one report received at %s whose body is %s", bodiesOf(got), err, received, tc.want)
			}
		})
	}
}

func TestNodeReports_AddToFailsOnAFailedReadAndOnABodyThatIsNoJSONObject(t *testing.T) {
	boom := errors.New("platform: 503")
	cases := []struct {
		name     string
		failWith error
		body     string
	}{
		{"the read fails", boom, `{}`},
		{"a body that is a JSON array", nil, `[]`},
		{"a null body", nil, `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reports := gitopstest.NewNodeReports()
			reports.Report("sub-cluster", verbAt, gitopstest.Application{Name: "app-alpha"})
			if tc.failWith != nil {
				reports.FailWith(tc.failWith)
			}

			got, err := reports.AddTo(context.Background(), contract.Desired{}, "sub-cluster", []observe.NodeReport{{ReceivedAt: verbAt, Body: json.RawMessage(tc.body)}})

			if err == nil || got != nil || (tc.failWith != nil && !errors.Is(err, tc.failWith)) {
				t.Errorf("AddTo = %q, %v; want no report and an error, wrapping %v when the read failed", bodiesOf(got), err, tc.failWith)
			}
		})
	}
}

// blockReports is a block's own source of node reports: one report made at madeAt listing what the block reads, carrying the Applications argo shows.
type blockReports struct {
	argo   *gitopstest.NodeReports
	madeAt time.Time
}

func (b blockReports) NodeReports(ctx context.Context, d contract.Desired, cluster string) ([]observe.NodeReport, error) {
	body, err := json.Marshal(map[string]any{"madeAt": b.madeAt, "subscriptionResources": []any{}})
	if err != nil {
		return nil, err
	}
	return b.argo.AddTo(ctx, d, cluster, []observe.NodeReport{{ReceivedAt: b.madeAt, Body: body}})
}

func TestNodeReports_AddToLetsCheckJudgeTheBlocksObjectsFromAReportTheBlockBuilt(t *testing.T) {
	cases := []struct {
		name    string
		removal bool
		set     []gitopstest.Object
		madeAt  time.Duration
		want    gitops.Verdict
		detail  string
	}{
		{name: "the block's Deployment reads Degraded", set: []gitopstest.Object{{Object: web, Sync: "Synced", Health: "Degraded", Message: "back-off pulling the image"}},
			madeAt: 30 * time.Second, want: gitops.VerdictFailed, detail: web.String() + " reads Degraded: back-off pulling the image"},
		{name: "its objects read Synced and Healthy beside a sibling's Degraded Job no file of the block declares", set: []gitopstest.Object{{Object: siblingJob, Sync: "Synced", Health: "Degraded", Message: "deadline exceeded"}},
			madeAt: 30 * time.Second, want: gitops.VerdictSucceeded, detail: "each of the 2 objects the block's files declare reads Synced"},
		{name: "the block's own report predates the verb, though the Applications it carries are newer",
			madeAt: -30 * time.Second, want: gitops.VerdictPending, detail: "no node report made since the verb"},
		{name: "a removal stays pending while the view lists the Deployment head no longer declares", removal: true, set: []gitopstest.Object{{Object: web, Sync: "OutOfSync", Health: "Healthy"}},
			madeAt: 30 * time.Second, want: gitops.VerdictPending, detail: web.String() + ", which the commit removed, is still among the objects it applied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, revision, removed := webThenRemoved(t)
			if tc.removal {
				revision = removed
			}
			argo := gitopstest.NewNodeReports()
			argo.Applies("sub-cluster", gitopstest.Applied{Name: "app-alpha", Repo: repo, Revision: revision, Paths: appPaths, Set: tc.set})
			d := gitops.Deployment{Repository: repo, Notifications: gitopstest.NewNotifications(), NodeReports: blockReports{argo: argo, madeAt: verbAt.Add(tc.madeAt)},
				Desired: contract.Desired{VerbAt: verbAt}, Application: "app-alpha", Cluster: "sub-cluster", Paths: appPaths}

			out, err := gitops.Check(context.Background(), d, revision)

			if err != nil || out.Verdict != tc.want || !strings.Contains(out.Detail, tc.detail) {
				t.Errorf("Check(%s) = %s (%s), %v; want %s naming %q", revision, out.Verdict, out.Detail, err, tc.want, tc.detail)
			}
		})
	}
}

func TestRepository_ServeGitHubCommitAnswersEachCommitAsTheKitsGitHubRepositoryReadsIt(t *testing.T) {
	repo, deployed, removed := webThenRemoved(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/gitops/commits/{sha}", repo.ServeGitHubCommit)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	github := &gitops.GitHubRepository{Client: ghapi.NewClient(srv.URL, "token", srv.Client()), Repo: "acme/gitops"}
	cases := []struct {
		name, revision, parent string
		paths                  []string
	}{
		{"the first commit, which has no parent", deployed, "", []string{"app/account.yaml", "app/deployment.json"}},
		{"a commit deleting one file and adding another", removed, deployed, []string{"app/deployment.json", "sibling/job.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent, paths, err := github.Changed(context.Background(), tc.revision)

			if err != nil || parent != tc.parent || !reflect.DeepEqual(paths, tc.paths) {
				t.Errorf("Changed(%s) through GitHub = %q, %q, %v; want %q, %q", tc.revision, parent, paths, err, tc.parent, tc.paths)
			}
		})
	}
}

func TestRepository_ServeGitHubCommitAnswers422AsGitHubDoesForACommitTheRepositoryNeverHeld(t *testing.T) {
	repo, _, _ := webThenRemoved(t)
	unknown := strings.Repeat("f", 40)
	w := httptest.NewRecorder()

	repo.ServeGitHubCommit(w, httptest.NewRequest(http.MethodGet, "/repos/acme/gitops/commits/"+unknown, nil))

	var body struct{ Message string }
	if w.Code != http.StatusUnprocessableEntity || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Message != "No commit found for SHA: "+unknown {
		t.Errorf("ServeGitHubCommit answered %d %s, want 422 with GitHub's message %q", w.Code, w.Body, "No commit found for SHA: "+unknown)
	}
}
