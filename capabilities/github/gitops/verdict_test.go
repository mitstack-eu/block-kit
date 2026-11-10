package gitops_test

// Check judges a block's commit by the objects its files declare, as the newest node report made since the verb shows them in the Argo CD Application at that commit or a later revision holding its files, and a sibling's objects decide nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/gitops"
	"github.com/mitstack-eu/block-kit/capabilities/github/gitops/gitopstest"
	"github.com/mitstack-eu/block-kit/contract"
)

const (
	homeApp     = "app-home"
	homeCluster = "sub-cluster"
	dbPath      = "db/cluster.json"
	certPath    = "db/certificate.json"
	apiPath     = "api/deployment.json"
)

var (
	verbAt = time.Date(2026, 11, 9, 9, 46, 0, 0, time.UTC)
	dbObj  = gitops.Object{Group: "postgresql.cnpg.io", Kind: "Cluster", Namespace: "app-home-db", Name: "postgres"}
	cert   = gitops.Object{Group: "cert-manager.io", Kind: "Certificate", Namespace: "app-home-db", Name: "postgres-server"}
	api    = gitops.Object{Group: "apps", Kind: "Deployment", Namespace: "app-home", Name: "api"}
)

// manifest renders o as a block renders its JSON manifests.
func manifest(o gitops.Object) []byte {
	apiVersion := "v1"
	if o.Group != "" {
		apiVersion = o.Group + "/v1"
	}
	raw, _ := json.Marshal(map[string]any{"apiVersion": apiVersion, "kind": o.Kind, "metadata": map[string]string{"name": o.Name, "namespace": o.Namespace}})
	return raw
}

func state(o gitops.Object, sync, health, message string) gitopstest.Object {
	return gitopstest.Object{Object: o, Sync: sync, Health: health, Message: message}
}

// home is an app's GitOps repository in which a sibling's Deployment landed before the block's own Cluster and Certificate.
type home struct {
	repo          *gitopstest.Repository
	sibling, own  string
	later         string
	notes         *gitopstest.Notifications
	reports       *gitopstest.NodeReports
	notifications gitops.Notifications
}

func newHome(t *testing.T) *home {
	t.Helper()
	h := &home{repo: gitopstest.NewRepository(), notes: gitopstest.NewNotifications(), reports: gitopstest.NewNodeReports()}
	h.notifications = h.notes
	h.sibling = h.commit(t, map[string][]byte{apiPath: manifest(api)})
	h.own = h.commit(t, map[string][]byte{dbPath: manifest(dbObj), certPath: manifest(cert)})
	return h
}

func (h *home) commit(t *testing.T, files map[string][]byte) string {
	t.Helper()
	head, _ := h.repo.Head(context.Background())
	rev, err := h.repo.Commit(context.Background(), head, files, "commit")
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

func (h *home) notify(revision, phase, health, message string) {
	h.notes.Set(gitops.Notification{App: homeApp, Revision: revision, Phase: phase, Health: health, Message: message})
}

// report adds a node report made a minute after the verb, showing the Application at revision with objects.
func (h *home) report(revision string, objects ...gitopstest.Object) {
	h.reports.Report(homeCluster, verbAt.Add(time.Minute), gitopstest.Application{Name: homeApp, Revision: revision, Objects: objects})
}

func (h *home) healthyAt(revision string) {
	h.report(revision, gitopstest.Healthy(dbObj), gitopstest.Healthy(cert), gitopstest.Healthy(api))
}

func (h *home) deployment() gitops.Deployment {
	return gitops.Deployment{
		Repository: h.repo, Notifications: h.notifications, NodeReports: h.reports,
		Desired: contract.Desired{VerbAt: verbAt}, Application: homeApp, Cluster: homeCluster, Paths: []string{certPath, dbPath},
	}
}

func (h *home) check(t *testing.T, revision string) gitops.Outcome {
	t.Helper()
	out, err := gitops.Check(context.Background(), h.deployment(), revision)
	if err != nil {
		t.Fatalf("Check(%s) error = %v, want a verdict", revision, err)
	}
	return out
}

// named replaces @own, @sibling and @later in want with the revisions they stand for.
func (h *home) named(want string) string {
	return strings.NewReplacer("@own", h.own, "@sibling", h.sibling, "@later", h.later).Replace(want)
}

type verdictCase struct {
	name    string
	arrange func(t *testing.T, h *home)
	want    gitops.Verdict
	detail  []string
}

func runVerdicts(t *testing.T, cases []verdictCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHome(t)
			tc.arrange(t, h)

			out := h.check(t, h.own)

			if out.Verdict != tc.want || out.Revision != h.own {
				t.Fatalf("Check(%s) = %+v; want %s on the block's own commit", h.own, out, tc.want)
			}
			for _, want := range tc.detail {
				if !strings.Contains(out.Detail, h.named(want)) {
					t.Errorf("Detail = %q, want it to name %q", out.Detail, h.named(want))
				}
			}
			if out.Detail == "" || strings.Contains(out.Detail, `phase ""`) || strings.Contains(out.Detail, `health ""`) {
				t.Errorf("Detail = %q, want it to say why without an empty phase or health", out.Detail)
			}
		})
	}
}

func TestCheck_JudgesOnlyTheObjectsTheBlocksFilesDeclare(t *testing.T) {
	runVerdicts(t, []verdictCase{
		{"a sibling's Degraded Deployment beside the block's Progressing Cluster is pending, naming the Cluster", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Degraded", "successfully synced (all tasks run)")
			h.report(h.own, state(dbObj, "Synced", "Progressing", "Setting up primary"), gitopstest.Healthy(cert), state(api, "Synced", "Degraded", "Back-off pulling image"))
		}, gitops.VerdictPending, []string{"Cluster.postgresql.cnpg.io app-home-db/postgres reads health Progressing: Setting up primary"}},
		{"the block's own Cluster reading Degraded fails, naming it and Argo CD's message", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Degraded", "successfully synced (all tasks run)")
			h.report(h.own, state(dbObj, "Synced", "Degraded", "the storage class local-path is missing"), gitopstest.Healthy(cert), gitopstest.Healthy(api))
		}, gitops.VerdictFailed, []string{"Cluster.postgresql.cnpg.io app-home-db/postgres reads Degraded: the storage class local-path is missing"}},
		{"every object of the block Synced and Healthy beside a Degraded sibling succeeds", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Degraded", "successfully synced (all tasks run)")
			h.report(h.own, gitopstest.Healthy(dbObj), gitopstest.Healthy(cert), state(api, "Synced", "Degraded", "Back-off pulling image"))
		}, gitops.VerdictSucceeded, []string{homeApp, "@own"}},
		{"an object of a kind Argo CD runs no health check for counts as Healthy", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.report(h.own, gitopstest.Healthy(dbObj), state(cert, "Synced", "", ""))
		}, gitops.VerdictSucceeded, nil},
		{"a sync that failed on a sibling's object only leaves the block's Synced and Healthy objects to decide", func(_ *testing.T, h *home) {
			h.notify(h.own, "Failed", "Degraded", "one or more objects failed to apply: Deployment.apps api is invalid")
			h.report(h.own, gitopstest.Healthy(dbObj), gitopstest.Healthy(cert), state(api, "OutOfSync", "Missing", ""))
		}, gitops.VerdictSucceeded, nil},
		{"a failed sync with an object of the block OutOfSync fails, naming the sync and the object", func(_ *testing.T, h *home) {
			h.notify(h.own, "Failed", "Degraded", "one or more objects failed to apply")
			h.report(h.own, gitopstest.Healthy(dbObj), state(cert, "OutOfSync", "Missing", ""))
		}, gitops.VerdictFailed, []string{"phase Failed: one or more objects failed to apply", "Certificate.cert-manager.io app-home-db/postgres-server reads sync OutOfSync"}},
		{"an object of the block OutOfSync without a failed sync is pending, naming it", func(_ *testing.T, h *home) {
			h.notify(h.own, "Running", "Progressing", "")
			h.report(h.own, gitopstest.Healthy(dbObj), state(cert, "OutOfSync", "Missing", ""))
		}, gitops.VerdictPending, []string{"Certificate.cert-manager.io app-home-db/postgres-server reads sync OutOfSync"}},
		{"an object of the block the Application does not list is pending, naming it", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.report(h.own, gitopstest.Healthy(dbObj), gitopstest.Healthy(api))
		}, gitops.VerdictPending, []string{"Certificate.cert-manager.io app-home-db/postgres-server is not among the objects"}},
		{"an object whose manifest names no namespace, which Argo CD applied in one, is pending, naming the namespace to write", func(t *testing.T, h *home) {
			unnamed := cert
			unnamed.Namespace = ""
			h.own = h.commit(t, map[string][]byte{certPath: manifest(unnamed)})
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.report(h.own, gitopstest.Healthy(dbObj), gitopstest.Healthy(cert))
		}, gitops.VerdictPending, []string{"Certificate.cert-manager.io postgres-server is not among the objects it applied, though it applied that kind and name in namespace app-home-db"}},
		{"an object of the block Missing is pending, naming it", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Missing", "")
			h.report(h.own, state(dbObj, "Synced", "Missing", ""), gitopstest.Healthy(cert))
		}, gitops.VerdictPending, []string{"Cluster.postgresql.cnpg.io app-home-db/postgres reads health Missing"}},
	})
}

func TestCheck_CountsOnlyAReportMadeSinceTheVerbShowingARevisionThatHoldsTheCommit(t *testing.T) {
	runVerdicts(t, []verdictCase{
		{"a node report older than the verb is pending, naming the missing report", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.reports.Report(homeCluster, verbAt.Add(-time.Minute), gitopstest.Application{Name: homeApp, Revision: h.own, Objects: []gitopstest.Object{gitopstest.Healthy(dbObj), gitopstest.Healthy(cert)}})
		}, gitops.VerdictPending, []string{homeCluster, "no node report made since the verb has arrived"}},
		{"a newer report made after the verb then decides", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.reports.Report(homeCluster, verbAt.Add(-time.Minute), gitopstest.Application{Name: homeApp, Revision: h.own, Objects: []gitopstest.Object{state(dbObj, "Synced", "Degraded", "old")}})
			h.healthyAt(h.own)
		}, gitops.VerdictSucceeded, nil},
		{"a report from a kit release that carries no revision is pending, naming the missing revision", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.reports.Report(homeCluster, verbAt.Add(time.Minute), gitopstest.Application{Name: homeApp, RevisionLeftOut: true, Objects: []gitopstest.Object{gitopstest.Healthy(dbObj), gitopstest.Healthy(cert)}})
		}, gitops.VerdictPending, []string{"carries no revision of Argo CD Application " + homeApp}},
		{"a report whose Argo CD compared no revision is pending, naming that", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.report("", gitopstest.Healthy(dbObj), gitopstest.Healthy(cert))
		}, gitops.VerdictPending, []string{"compared no revision of Application " + homeApp}},
		{"a report at an earlier revision is pending, naming that revision", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.report(h.sibling, state(api, "Synced", "Healthy", ""))
		}, gitops.VerdictPending, []string{"at @sibling, which does not hold the block's commit @own"}},
		{"a report at a sibling's later commit holding the block's files counts", func(t *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.later = h.commit(t, map[string][]byte{apiPath: append(manifest(api), '\n')})
			h.healthyAt(h.later)
		}, gitops.VerdictSucceeded, []string{homeApp + " at @later"}},
		{"a report at a later commit that changed the block's file is pending, naming the commit and the file", func(t *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.healthyAt(h.commit(t, map[string][]byte{certPath: nil}))
		}, gitops.VerdictPending, []string{"after the block's own @own changed " + certPath}},
		{"a report from a kit release that carries no health is pending, naming the missing health", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.report(h.own, gitopstest.Object{Object: dbObj, Sync: "Synced", HealthLeftOut: true}, gitopstest.Healthy(cert))
		}, gitops.VerdictPending, []string{"Cluster.postgresql.cnpg.io app-home-db/postgres carries no health"}},
		{"a report whose list of applied objects was cut to fit its size is pending, naming the missing list", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.report(h.own)
		}, gitops.VerdictPending, []string{"carries no list of the objects Argo CD Application " + homeApp + " applied"}},
		{"a report naming no such Application is pending, naming it", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.reports.Report(homeCluster, verbAt.Add(time.Minute), gitopstest.Application{Name: "cluster-root", Revision: h.own, Objects: []gitopstest.Object{gitopstest.Healthy(dbObj), gitopstest.Healthy(cert)}})
		}, gitops.VerdictPending, []string{`names no Argo CD Application "` + homeApp + `"`}},
		{"the reports of another cluster are never read", func(_ *testing.T, h *home) {
			h.notify(h.own, "Succeeded", "Healthy", "")
			h.reports.Report("sub-other", verbAt.Add(time.Minute), gitopstest.Application{Name: homeApp, Revision: h.own, Objects: []gitopstest.Object{gitopstest.Healthy(dbObj), gitopstest.Healthy(cert)}})
		}, gitops.VerdictPending, []string{"no node report made since the verb has arrived"}},
	})
}

func TestCheck_ARemovalSucceedsOnceTheApplicationNoLongerListsTheObjectsItRemoved(t *testing.T) {
	cases := []struct {
		name   string
		listed []gitopstest.Object
		want   gitops.Verdict
		detail string
	}{
		{"the removed Cluster still listed for pruning is pending, naming it", []gitopstest.Object{state(dbObj, "OutOfSync", "Healthy", ""), gitopstest.Healthy(api)},
			gitops.VerdictPending, "Cluster.postgresql.cnpg.io app-home-db/postgres, which the commit removed, is still among the objects"},
		{"no removed object listed succeeds", []gitopstest.Object{gitopstest.Healthy(api)}, gitops.VerdictSucceeded, homeApp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHome(t)
			removal := h.commit(t, map[string][]byte{dbPath: nil, certPath: nil})
			h.notify(removal, "Succeeded", "Healthy", "")
			h.report(removal, tc.listed...)

			out := h.check(t, removal)

			if out.Verdict != tc.want || out.Revision != removal || !strings.Contains(out.Detail, tc.detail) {
				t.Errorf("Check(%s) = %+v; want %s on the removal, detail naming %q", removal, out, tc.want, tc.detail)
			}
		})
	}
}

func TestCheck_RefusesADeploymentMissingASourceOrItsPathsOrARevision(t *testing.T) {
	h := newHome(t)
	noReports, noPaths := h.deployment(), h.deployment()
	noReports.NodeReports, noPaths.Paths = nil, nil
	cases := []struct {
		name     string
		d        gitops.Deployment
		revision string
	}{
		{"no node reports", noReports, h.own},
		{"no paths, which would judge no object and succeed on none", noPaths, h.own},
		{"no revision", h.deployment(), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := gitops.Check(context.Background(), tc.d, tc.revision)

			if err == nil || out.Verdict != gitops.VerdictPending {
				t.Errorf("Check = %+v, %v; want pending with an error", out, err)
			}
		})
	}
}

func TestCheck_ReturnsTheNodeReportsError(t *testing.T) {
	h := newHome(t)
	h.notify(h.own, "Succeeded", "Healthy", "")
	boom := errors.New("platform: 503")
	h.reports.FailWith(boom)

	_, err := gitops.Check(context.Background(), h.deployment(), h.own)

	if !errors.Is(err, boom) {
		t.Errorf("Check error = %v, want the node reports' error", err)
	}
}
