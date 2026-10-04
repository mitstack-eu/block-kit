package gitops_test

// resource_test.go pins gitops.Resource, one declared resource served
// through the kit's per-resource hooks:
//
//   - EnsureWithReport renders, refuses a secret in any rendered file as a
//     secret-in-manifest refusal and a path holding a comma as an
//     invalid-manifest-path refusal, commits through CommitManifests, and
//     reports the app, the revision and the sorted paths; an unchanged
//     ensure reports the recorded revision while its files still match, and
//     head otherwise;
//   - Observe first checks head: files at head that differ from the render,
//     or stand only in part, are unknown naming the paths, and an empty
//     repository is absent with no notification read. Otherwise it reads
//     the app's newest notification, whatever its revision, and checks the
//     files at that notification's revision: when they match what head
//     holds, the notification judges the resource (succeeded is present or
//     absent, pending is unknown, failed is an error); when they do not,
//     Argo CD has not yet reported a revision carrying the change, which is
//     unknown. No notification at all is unknown. It never takes the
//     revision to judge from ResourceIdentity;
//   - Delete commits removal of the recorded paths, or the rendered ones
//     when none are recorded, and commits nothing when they are already
//     gone;
//   - a bare Observe or Delete builds its Desired from the identity alone;
//   - one app's ensure and delete never touch another app's paths.

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/gitops"
	"github.com/mitstack-eu/operator-kit/handler"
)

var (
	_ handler.DeclaredResource  = (*gitops.Resource)(nil)
	_ handler.ReportingResource = (*gitops.Resource)(nil)
	_ handler.RequestObserver   = (*gitops.Resource)(nil)
	_ handler.RequestDeleter    = (*gitops.Resource)(nil)
)

func ensure(t *testing.T, b *gitopsBlock, d contract.Desired) contract.Ensured {
	t.Helper()
	got, err := b.resource.EnsureWithReport(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureWithReport: %v", err)
	}
	return got
}

func observe(t *testing.T, b *gitopsBlock, d contract.Desired) contract.Observation {
	t.Helper()
	obs, err := b.resource.ObserveWithRequest(context.Background(), d)
	if err != nil {
		t.Fatalf("ObserveWithRequest: %v", err)
	}
	return obs
}

func TestResource_EnsureWithReportCommitsTheRenderedFilesAndReportsAppRevisionAndSortedPaths(t *testing.T) {
	b := newNamespaceBlock()

	got := ensure(t, b, desiredFor("app-alpha"))

	commits := b.repo.Commits()
	if len(commits) != 1 {
		t.Fatalf("Commits = %d, want 1", len(commits))
	}
	want := contract.Ensured{
		Identity: map[string]string{"app": "app-alpha", "revision": commits[0].Revision, "paths": alphaPaths},
		Outputs:  alphaOutputs(),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnsureWithReport = %+v, want %+v", got, want)
	}
	wantFilesAt(t, b.repo, commits[0].Revision, renderApp("app-alpha", "k3s-fsn1").Files)
}

func TestResource_EnsureCommitsTheSameWayEnsureWithReportDoes(t *testing.T) {
	b := newNamespaceBlock()

	if err := b.resource.Ensure(context.Background(), desiredFor("app-alpha")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := len(b.repo.Commits()); got != 1 {
		t.Fatalf("Commits = %d, want 1", got)
	}
	wantFilesAt(t, b.repo, b.head(t), renderApp("app-alpha", "k3s-fsn1").Files)
}

func TestResource_ARepeatedEnsureCommitsNothingAndReportsTheSameRevision(t *testing.T) {
	b := newNamespaceBlock()
	first := ensure(t, b, desiredFor("app-alpha"))

	second := ensure(t, b, desiredFor("app-alpha"))

	if got := len(b.repo.Commits()); got != 1 {
		t.Errorf("Commits = %d, want 1: an unchanged ensure lands nothing", got)
	}
	if !reflect.DeepEqual(second, first) {
		t.Errorf("second EnsureWithReport = %+v, want the first's %+v", second, first)
	}
}

func TestResource_AnUnchangedEnsureReportsTheRecordedRevisionWhileItsFilesStillMatch(t *testing.T) {
	b := newNamespaceBlock()
	first := ensure(t, b, desiredFor("app-alpha"))
	ensure(t, b, desiredFor("app-beta")) // head moves past app-alpha's commit

	recorded := desiredFor("app-alpha")
	recorded.ResourceIdentity = first.Identity
	got := ensure(t, b, recorded)

	if got.Identity["revision"] != first.Identity["revision"] {
		t.Errorf("revision = %q, want the recorded %q: nothing changed and its files still match", got.Identity["revision"], first.Identity["revision"])
	}
	if n := len(b.repo.Commits()); n != 2 {
		t.Errorf("Commits = %d, want 2", n)
	}

	unrecorded := ensure(t, b, desiredFor("app-alpha"))
	if head := b.head(t); unrecorded.Identity["revision"] != head {
		t.Errorf("with no recorded identity, revision = %q, want head %q", unrecorded.Identity["revision"], head)
	}
}

func TestResource_AnUnchangedEnsureReportsHeadWhenTheRecordedRevisionsFilesNoLongerMatch(t *testing.T) {
	b := newNamespaceBlock()
	stale := ensure(t, b, desiredFor("app-alpha"))
	moved := desiredFor("app-alpha")
	moved.ResourceInputs["cluster"] = []byte(`"k3s-nbg1"`)
	ensure(t, b, moved)

	again := desiredFor("app-alpha")
	again.ResourceInputs["cluster"] = []byte(`"k3s-nbg1"`)
	again.ResourceIdentity = stale.Identity
	got := ensure(t, b, again)

	if head := b.head(t); got.Identity["revision"] != head {
		t.Errorf("revision = %q, want head %q: the recorded revision holds other content", got.Identity["revision"], head)
	}
	if n := len(b.repo.Commits()); n != 2 {
		t.Errorf("Commits = %d, want 2", n)
	}
}

// withExtraFile wraps render to add one more file, as a block that put a
// value where it does not belong would.
func withExtraFile(render func(contract.Desired) (gitops.Rendered, error), path string, content func(contract.Desired) []byte) func(contract.Desired) (gitops.Rendered, error) {
	return func(d contract.Desired) (gitops.Rendered, error) {
		r, err := render(d)
		if err != nil {
			return r, err
		}
		r.Files[path] = content(d)
		return r, nil
	}
}

func TestResource_EnsureRefusesASecretInARenderedFileAsSecretInManifestAndCommitsNothing(t *testing.T) {
	const leaky = "apps/app-alpha/pull-secret.yaml"
	for name, c := range map[string]struct {
		secret  string
		content func(contract.Desired) []byte
	}{
		"the github token verbatim": {githubToken, func(d contract.Desired) []byte {
			return []byte("auth: " + d.Secrets["github"]["token"] + "\n")
		}},
		"a secret input base64-encoded": {secretInput, func(d contract.Desired) []byte {
			return []byte("data:\n  password: " + base64.StdEncoding.EncodeToString([]byte(d.SecretInputs["dbPassword"])) + "\n")
		}},
		"an app secret verbatim": {appSecret, func(d contract.Desired) []byte {
			return []byte("STRIPE_KEY: " + d.AppSecrets["STRIPE_KEY"] + "\n")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBlock(withExtraFile(renderNamespace, leaky, c.content), "app-alpha")

			_, err := b.resource.EnsureWithReport(context.Background(), desiredFor("app-alpha"))

			var refused *handler.Refused
			if !errors.As(err, &refused) || refused.Class != "secret-in-manifest" {
				t.Fatalf("err = %v, want a *handler.Refused of class secret-in-manifest", err)
			}
			if !strings.Contains(err.Error(), leaky) {
				t.Errorf("error %q does not name the path %s", err, leaky)
			}
			if strings.Contains(err.Error(), c.secret) || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte(c.secret))) {
				t.Error("the refusal carries the secret value")
			}
			if got := len(b.repo.Commits()); got != 0 {
				t.Errorf("Commits = %d, want 0", got)
			}
		})
	}
}

func TestResource_EnsureRefusesARenderedPathHoldingACommaAsInvalidManifestPathAndCommitsNothing(t *testing.T) {
	const comma = "apps/app-alpha/role,binding.yaml"
	b := newBlock(withExtraFile(renderNamespace, comma, func(contract.Desired) []byte {
		return []byte("kind: RoleBinding\n")
	}), "app-alpha")

	_, err := b.resource.EnsureWithReport(context.Background(), desiredFor("app-alpha"))

	var refused *handler.Refused
	if !errors.As(err, &refused) || refused.Class != "invalid-manifest-path" {
		t.Fatalf("err = %v, want a *handler.Refused of class invalid-manifest-path: the identity joins paths with commas", err)
	}
	if !strings.Contains(err.Error(), comma) {
		t.Errorf("error %q does not name the path %s", err, comma)
	}
	if got := len(b.repo.Commits()); got != 0 {
		t.Errorf("Commits = %d, want 0", got)
	}
}

func TestResource_ObserveIsPresentWithTheOutputsWhenTheFilesStandAtHeadAndItsDeploySucceeded(t *testing.T) {
	b := newNamespaceBlock()
	ensure(t, b, desiredFor("app-alpha"))

	obs := observe(t, b, desiredFor("app-alpha"))

	if obs.State != contract.Present || !reflect.DeepEqual(obs.Outputs, alphaOutputs()) {
		t.Errorf("Observe = %+v, want present with %v", obs, alphaOutputs())
	}
}

func TestResource_ObserveIsUnknownWithADetailWhileHeadsDeployIsPendingOrUnreported(t *testing.T) {
	for name, seed := range map[string]func(b *gitopsBlock, rev string){
		"no notification yet": func(*gitopsBlock, string) {},
		"syncing": func(b *gitopsBlock, rev string) {
			b.notes.Set(gitops.Notification{App: "app-alpha", Revision: rev, Phase: "Running", Health: "Progressing"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBlock(renderNamespace)
			rev := ensure(t, b, desiredFor("app-alpha")).Identity["revision"]
			seed(b, rev)

			obs := observe(t, b, desiredFor("app-alpha"))

			if obs.State != contract.Unknown || obs.StatusDetail == "" {
				t.Errorf("Observe = %+v, want unknown with a detail", obs)
			}
		})
	}
}

func TestResource_ObserveFailsCarryingPhaseHealthAndMessageWhenHeadsDeployFailed(t *testing.T) {
	b := newBlock(renderNamespace)
	rev := ensure(t, b, desiredFor("app-alpha")).Identity["revision"]
	b.notes.Set(gitops.Notification{App: "app-alpha", Revision: rev, Phase: "Failed", Health: "Degraded", Message: "namespace quota exceeded"})

	_, err := b.resource.ObserveWithRequest(context.Background(), desiredFor("app-alpha"))

	if err == nil {
		t.Fatal("ObserveWithRequest returned no error for a failed deploy")
	}
	for _, want := range []string{"Failed", "Degraded", "namespace quota exceeded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}
}

func TestResource_ObserveJudgesTheNewestNotificationNotTheRevisionResourceIdentityRecords(t *testing.T) {
	b := newBlock(renderNamespace)
	stale := ensure(t, b, desiredFor("app-alpha"))
	moved := desiredFor("app-alpha")
	moved.ResourceInputs["cluster"] = []byte(`"k3s-nbg1"`)
	head := ensure(t, b, moved).Identity["revision"]
	b.notes.Set(gitops.Notification{App: "app-alpha", Revision: stale.Identity["revision"], Phase: "Failed", Health: "Degraded"})
	b.notes.Set(gitops.Notification{App: "app-alpha", Revision: head, Phase: "Succeeded", Health: "Healthy"})

	moved.ResourceIdentity = stale.Identity
	obs := observe(t, b, moved)

	if obs.State != contract.Present {
		t.Errorf("Observe = %+v, want present: head %s holds the files and its deploy succeeded", obs, head)
	}
}

func TestResource_ObserveIsAbsentOnAnEmptyRepository(t *testing.T) {
	b := newBlock(renderNamespace)

	if obs := observe(t, b, desiredFor("app-alpha")); obs.State != contract.Absent {
		t.Errorf("Observe = %+v, want absent", obs)
	}
	if got := b.notes.Reads("app-alpha", ""); got != 0 {
		t.Errorf("Observe read the app's newest notification %d time(s), want none: an empty repository is absent with no read", got)
	}
}

func TestResource_ObserveIsPresentWhenTheOnlyNotificationNamesALaterRevisionStillCarryingTheFiles(t *testing.T) {
	b := newBlock(renderNamespace)
	ensure(t, b, desiredFor("app-alpha"))
	later := ensure(t, b, desiredFor("app-beta")).Identity["revision"] // another app's commit lands after ours
	b.notes.Set(gitops.Notification{App: "app-alpha", Project: "app-alpha", Revision: later, Phase: "Succeeded", Health: "Healthy"})
	ensure(t, b, desiredFor("app-gamma")) // head moves on again, with no notification for it yet

	obs := observe(t, b, desiredFor("app-alpha"))

	if obs.State != contract.Present || !reflect.DeepEqual(obs.Outputs, alphaOutputs()) {
		t.Errorf("Observe = %+v, want present with %v: Argo CD synced %s, which carries our files", obs, alphaOutputs(), later)
	}
}

func TestResource_ObserveIsUnknownWhenTheNewestNotificationNamesARevisionOlderThanTheChange(t *testing.T) {
	b := newBlock(renderNamespace)
	older := ensure(t, b, desiredFor("app-alpha")).Identity["revision"]
	b.notes.Set(gitops.Notification{App: "app-alpha", Project: "app-alpha", Revision: older, Phase: "Succeeded", Health: "Healthy"})
	changed := desiredFor("app-alpha")
	changed.ResourceInputs["cluster"] = []byte(`"k3s-nbg1"`)
	ensure(t, b, changed)

	obs, err := b.resource.ObserveWithRequest(context.Background(), changed)

	if err != nil {
		t.Fatalf("ObserveWithRequest: %v, want unknown, not an error", err)
	}
	if obs.State != contract.Unknown || !strings.Contains(obs.StatusDetail, "Argo CD") {
		t.Errorf("Observe = %+v, want unknown with a detail saying Argo CD has not yet reported a revision carrying the change", obs)
	}
}

func TestResource_ObserveAfterDeleteIsAbsentWhenTheOnlyNotificationNamesALaterRevisionWithoutTheFiles(t *testing.T) {
	b := newBlock(renderNamespace)
	ensure(t, b, desiredFor("app-alpha"))
	if err := b.resource.DeleteWithRequest(context.Background(), desiredFor("app-alpha")); err != nil {
		t.Fatalf("DeleteWithRequest: %v", err)
	}
	later := ensure(t, b, desiredFor("app-beta")).Identity["revision"]
	b.notes.Set(gitops.Notification{App: "app-alpha", Project: "app-alpha", Revision: later, Phase: "Succeeded", Health: "Healthy"})
	ensure(t, b, desiredFor("app-gamma")) // head moves on again, with no notification for it yet

	if obs := observe(t, b, desiredFor("app-alpha")); obs.State != contract.Absent {
		t.Errorf("Observe = %+v, want absent: Argo CD synced %s, which no longer carries our files", obs, later)
	}
}

func TestResource_ObserveAfterDeleteIsUnknownWhenTheNewestNotificationNamesARevisionStillCarryingTheFiles(t *testing.T) {
	b := newBlock(renderNamespace)
	before := ensure(t, b, desiredFor("app-alpha")).Identity["revision"]
	b.notes.Set(gitops.Notification{App: "app-alpha", Project: "app-alpha", Revision: before, Phase: "Succeeded", Health: "Healthy"})
	if err := b.resource.DeleteWithRequest(context.Background(), desiredFor("app-alpha")); err != nil {
		t.Fatalf("DeleteWithRequest: %v", err)
	}

	obs, err := b.resource.ObserveWithRequest(context.Background(), desiredFor("app-alpha"))

	if err != nil {
		t.Fatalf("ObserveWithRequest: %v, want unknown, not an error", err)
	}
	if obs.State != contract.Unknown || !strings.Contains(obs.StatusDetail, "Argo CD") {
		t.Errorf("Observe = %+v, want unknown with a detail saying Argo CD has not yet reported a revision carrying the removal", obs)
	}
}

func TestResource_ObserveAfterDeleteIsAbsentOnceHeadsDeploySucceeded(t *testing.T) {
	b := newNamespaceBlock()
	ensure(t, b, desiredFor("app-alpha"))
	if err := b.resource.DeleteWithRequest(context.Background(), desiredFor("app-alpha")); err != nil {
		t.Fatalf("DeleteWithRequest: %v", err)
	}

	if obs := observe(t, b, desiredFor("app-alpha")); obs.State != contract.Absent {
		t.Errorf("Observe = %+v, want absent", obs)
	}
}

func TestResource_ObserveAfterDeleteIsUnknownWhilePendingAndFailsWhenTheDeployFailed(t *testing.T) {
	for _, c := range []struct {
		name          string
		phase, health string
		wantErr       bool
	}{
		{"no notification", "", "", false},
		{"pending", "Running", "Progressing", false},
		{"failed", "Error", "Degraded", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newBlock(renderNamespace)
			ensure(t, b, desiredFor("app-alpha"))
			if err := b.resource.DeleteWithRequest(context.Background(), desiredFor("app-alpha")); err != nil {
				t.Fatalf("DeleteWithRequest: %v", err)
			}
			if c.phase != "" {
				b.notes.Set(gitops.Notification{App: "app-alpha", Revision: b.head(t), Phase: c.phase, Health: c.health, Message: "prune failed"})
			}

			obs, err := b.resource.ObserveWithRequest(context.Background(), desiredFor("app-alpha"))

			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), "prune failed") {
					t.Errorf("err = %v, want one carrying the notification's message", err)
				}
				return
			}
			if err != nil || obs.State != contract.Unknown || obs.StatusDetail == "" {
				t.Errorf("Observe = %+v, %v, want unknown with a detail", obs, err)
			}
		})
	}
}

func TestResource_ObserveIsUnknownNamingThePathWhenOnlySomeFilesStand(t *testing.T) {
	b := newNamespaceBlock()
	rev := ensure(t, b, desiredFor("app-alpha")).Identity["revision"]
	if _, err := b.repo.Commit(context.Background(), rev, map[string][]byte{alphaServiceAccountPath: nil}, "hand edit"); err != nil {
		t.Fatalf("hand edit: %v", err)
	}

	obs := observe(t, b, desiredFor("app-alpha"))

	if obs.State != contract.Unknown || !strings.Contains(obs.StatusDetail, alphaServiceAccountPath) {
		t.Errorf("Observe = %+v, want unknown naming %s", obs, alphaServiceAccountPath)
	}
}

func TestResource_ObserveIsUnknownNamingThePathWhenAFileStandsWithOtherContent(t *testing.T) {
	b := newNamespaceBlock()
	rev := ensure(t, b, desiredFor("app-alpha")).Identity["revision"]
	if _, err := b.repo.Commit(context.Background(), rev, map[string][]byte{alphaNamespacePath: []byte("kind: Namespace\n# drifted\n")}, "hand edit"); err != nil {
		t.Fatalf("hand edit: %v", err)
	}

	obs := observe(t, b, desiredFor("app-alpha"))

	if obs.State != contract.Unknown || !strings.Contains(obs.StatusDetail, alphaNamespacePath) {
		t.Errorf("Observe = %+v, want unknown naming %s", obs, alphaNamespacePath)
	}
}

// identityRender renders from the app id alone and records each Desired it
// is handed, for the calls that receive only an Identity.
type identityRender struct{ seen []contract.Desired }

func (r *identityRender) render(d contract.Desired) (gitops.Rendered, error) {
	r.seen = append(r.seen, d)
	if d.AppID == "" {
		return gitops.Rendered{}, errors.New("no app id")
	}
	return renderApp(d.AppID, "k3s-fsn1"), nil
}

func TestResource_ObserveWithTheIdentityAloneRendersFromADesiredBuiltFromIt(t *testing.T) {
	r := &identityRender{}
	b := newBlock(r.render, "app-alpha")
	ensure(t, b, desiredFor("app-alpha"))
	id := desiredFor("app-alpha").Identity
	r.seen = nil

	obs, err := b.resource.Observe(context.Background(), id)

	if err != nil || obs.State != contract.Present || !reflect.DeepEqual(obs.Outputs, alphaOutputs()) {
		t.Fatalf("Observe = %+v, %v, want present with %v", obs, err, alphaOutputs())
	}
	if len(r.seen) == 0 {
		t.Fatal("Observe never rendered")
	}
	for _, d := range r.seen {
		if !reflect.DeepEqual(d, contract.Desired{Identity: id}) {
			t.Errorf("Render was handed %+v, want a Desired carrying the identity alone", d)
		}
	}
}

func TestResource_DeleteWithTheIdentityAloneRemovesTheRenderedPaths(t *testing.T) {
	r := &identityRender{}
	b := newBlock(r.render, "app-alpha")
	ensure(t, b, desiredFor("app-alpha"))

	if err := b.resource.Delete(context.Background(), desiredFor("app-alpha").Identity); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	wantFilesAt(t, b.repo, b.head(t), map[string][]byte{alphaNamespacePath: nil, alphaServiceAccountPath: nil})
}

func TestResource_DeleteWithRequestRemovesThePathsTheRecordedIdentityNamesWithoutRendering(t *testing.T) {
	b := newNamespaceBlock()
	reported := ensure(t, b, desiredFor("app-alpha"))
	b.resource.Render = func(contract.Desired) (gitops.Rendered, error) {
		return gitops.Rendered{}, errors.New("the render's inputs are gone")
	}

	d := desiredFor("app-alpha")
	d.ResourceIdentity = reported.Identity
	if err := b.resource.DeleteWithRequest(context.Background(), d); err != nil {
		t.Fatalf("DeleteWithRequest: %v", err)
	}

	commits := b.repo.Commits()
	if len(commits) != 2 {
		t.Fatalf("Commits = %d, want 2", len(commits))
	}
	var removed []string
	for path, content := range commits[1].Changes {
		if content != nil {
			t.Errorf("the delete commit writes %s, want it to only remove paths", path)
		}
		removed = append(removed, path)
	}
	sort.Strings(removed)
	if want := []string{alphaNamespacePath, alphaServiceAccountPath}; !reflect.DeepEqual(removed, want) {
		t.Errorf("the delete commit removes %q, want %q", removed, want)
	}
}

func TestResource_DeleteWithRequestRemovesTheRenderedPathsWhenNoneAreRecorded(t *testing.T) {
	b := newNamespaceBlock()
	ensure(t, b, desiredFor("app-alpha"))

	if err := b.resource.DeleteWithRequest(context.Background(), desiredFor("app-alpha")); err != nil {
		t.Fatalf("DeleteWithRequest: %v", err)
	}
	if got := len(b.repo.Commits()); got != 2 {
		t.Errorf("Commits = %d, want 2", got)
	}
	wantFilesAt(t, b.repo, b.head(t), map[string][]byte{alphaNamespacePath: nil, alphaServiceAccountPath: nil})
}

func TestResource_DeleteOfAResourceAlreadyGoneSucceedsWithNoCommit(t *testing.T) {
	b := newNamespaceBlock()
	if err := b.resource.DeleteWithRequest(context.Background(), desiredFor("app-alpha")); err != nil {
		t.Fatalf("DeleteWithRequest on an empty repository: %v", err)
	}
	if got := len(b.repo.Commits()); got != 0 {
		t.Errorf("Commits = %d after deleting nothing, want 0", got)
	}

	ensure(t, b, desiredFor("app-alpha"))
	for i := 1; i <= 2; i++ {
		if err := b.resource.DeleteWithRequest(context.Background(), desiredFor("app-alpha")); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	if got := len(b.repo.Commits()); got != 2 {
		t.Errorf("Commits = %d, want 2: the second delete finds the paths gone and lands nothing", got)
	}
}

func TestResource_OneAppsEnsureAndDeleteNeverTouchAnotherAppsPaths(t *testing.T) {
	b := newNamespaceBlock()
	ensure(t, b, desiredFor("app-alpha"))
	alpha := renderApp("app-alpha", "k3s-fsn1").Files

	ensure(t, b, desiredFor("app-beta"))
	if err := b.resource.DeleteWithRequest(context.Background(), desiredFor("app-beta")); err != nil {
		t.Fatalf("DeleteWithRequest(app-beta): %v", err)
	}

	for _, c := range b.repo.Commits()[1:] {
		for path := range c.Changes {
			if !strings.HasPrefix(path, "apps/app-beta/") {
				t.Errorf("app-beta's commit %q touches %s", c.Message, path)
			}
		}
	}
	wantFilesAt(t, b.repo, b.head(t), alpha)
	if obs := observe(t, b, desiredFor("app-alpha")); obs.State != contract.Present {
		t.Errorf("app-alpha observed %+v after app-beta came and went, want present", obs)
	}
}
