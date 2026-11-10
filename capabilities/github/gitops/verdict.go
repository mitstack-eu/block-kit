// Package gitops, an optional helper, commits the Kubernetes files a block renders to its GitOps repository on GitHub
// and judges the deploy by the objects those files declare, as the cluster's node reports show Argo CD applied them.
package gitops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

// Deployment is one block's deploy into an Argo CD Application with the sources its verdict reads, each required but Desired, whose zero VerbAt lets every report count.
type Deployment struct {
	Repository    Repository
	Notifications Notifications
	// NodeReports reads Cluster's node reports, usually observe.NodeReportsClient{}.
	NodeReports observe.NodeReportsAPI
	// Desired is the request, and only a node report made after its VerbAt counts.
	Desired contract.Desired
	// Application is the Argo CD Application that applies the block's files, as an identity's argocdApplication names it.
	Application string
	// Cluster is the subscription whose node reports carry Application, as an identity's clusterSubscription names it.
	Cluster string
	// Paths are the files the block owns, whose Kubernetes objects are what the verdict judges, and Deploy fills them from the files it commits.
	Paths []string
}

// Outcome is a verdict on the block's own commit and what it rests on.
type Outcome struct {
	Verdict Verdict
	// Revision is the one handed to Check when Argo CD notified a sync of it, and otherwise the block's own commit, the newest at or before it that changed one of its paths.
	Revision string
	// Judged is the revision the node report shows Argo CD compared, Revision or a later one holding its files as Revision does, and "" until a report counts.
	Judged string
	// Notification is the newest notification Check found at a revision holding the block's files as Revision does, the zero value when it found none.
	Notification Notification
	// Detail says what the verdict rests on, naming the object and Argo CD's message, or what a pending verdict waits for.
	Detail string
}

// Check judges the block's commit at revision by the objects d.Paths declare, as the newest node report of d.Cluster made since the verb shows them in d.Application at that commit or a later revision in which no commit changed one of d.Paths, since Argo CD's notification speaks for the whole Application.
func Check(ctx context.Context, d Deployment, revision string) (Outcome, error) {
	pending := Outcome{Verdict: VerdictPending, Revision: revision}
	if err := d.complete(revision); err != nil {
		return pending, err
	}
	head, err := d.Repository.Head(ctx)
	if err != nil {
		return pending, fmt.Errorf("gitops: reading head: %w", err)
	}
	line, err := walkLineage(ctx, d.Repository, head, revision, d.Paths)
	if err != nil {
		return pending, err
	}
	j := judgement{d: d, line: line, own: line.ownRevision(revision)}
	if j.notice, err = j.readNotice(ctx, revision); err != nil {
		return Outcome{Verdict: VerdictPending, Revision: j.own}, err
	}
	j.revision = j.own
	if j.notice.exact {
		j.revision = revision
	}
	if !j.notice.holds {
		return Outcome{Verdict: VerdictPending, Revision: j.revision, Detail: j.notice.describe(d.Application, j.own)}, nil
	}
	return j.fromReport(ctx)
}

func (d Deployment) complete(revision string) error {
	switch {
	case revision == "":
		return errors.New("gitops: no revision to judge")
	case d.Repository == nil || d.Notifications == nil || d.NodeReports == nil || d.Application == "" || d.Cluster == "" || len(d.Paths) == 0:
		return errors.New("gitops: a verdict needs the repository, the notifications, the node reports, the Argo CD Application, the cluster subscription and the block's paths")
	}
	return nil
}

// judgement is one Check of the block's own commit own, whose outcome names revision.
type judgement struct {
	d             Deployment
	line          lineage
	own, revision string
	notice        notice
}

// notice is the newest notification Check found, and whether its revision holds the block's files as its own commit does.
type notice struct {
	n            Notification
	found, holds bool
	// exact marks a notification of the revision handed to Check.
	exact bool
	// moved names the commit and path that keep a later notified revision from holding them.
	moved, path string
}

// readNotice reads the notification at revision, then at the block's own commit, then the Application's newest.
func (j judgement) readNotice(ctx context.Context, revision string) (notice, error) {
	for i, at := range slices.Compact([]string{revision, j.own}) {
		n, ok, err := j.d.Notifications.Latest(ctx, j.d.Application, at)
		if err != nil || ok {
			return notice{n: n, found: ok, holds: ok, exact: i == 0}, err
		}
	}
	n, ok, err := j.d.Notifications.Latest(ctx, j.d.Application, "")
	if err != nil || !ok {
		return notice{}, err
	}
	out := notice{n: n, found: true}
	if i := j.line.at(n.Revision); i >= 0 {
		out.moved, out.path = j.line.changedAfterOwn(i)
		out.holds = out.moved == ""
	}
	return out, nil
}

func (n notice) failedSync() bool {
	return n.holds && (n.n.Phase == "Error" || n.n.Phase == "Failed")
}

func (n notice) describe(app, own string) string {
	switch {
	case !n.found:
		return fmt.Sprintf("Argo CD has sent no notification of %s", app)
	case n.moved != "":
		return fmt.Sprintf("Argo CD's newest notification of %s is at %s, but commit %s after the block's own %s changed %s", app, n.n.Revision, n.moved, own, n.path)
	case !n.holds:
		return fmt.Sprintf("Argo CD sent no notification of %s at %s or a later revision holding the block's files, and its newest is at %s", app, own, n.n.Revision)
	}
	return ""
}

// fromReport reads the Application from the newest counting node report and judges the objects the block's files declare.
func (j judgement) fromReport(ctx context.Context) (Outcome, error) {
	out := Outcome{Verdict: VerdictPending, Revision: j.revision}
	if j.notice.holds {
		out.Notification = j.notice.n
	}
	app, why, err := observe.NodeReported{API: j.d.NodeReports}.ArgoCDApplication(ctx, j.d.Desired, j.d.Cluster, j.d.Application)
	if err != nil {
		return out, fmt.Errorf("gitops: reading the node reports of %s: %w", j.d.Cluster, err)
	}
	if why != "" {
		out.Detail = fmt.Sprintf("cluster subscription %s: %s", j.d.Cluster, why)
		return out, nil
	}
	if why = j.counts(app); why != "" {
		out.Detail = why
		return out, nil
	}
	out.Judged = app.Revision
	out.Verdict, why, err = j.judgeObjects(ctx, app)
	out.Detail = fmt.Sprintf("Argo CD Application %s at %s: %s", j.d.Application, app.Revision, why)
	return out, err
}

// counts says why the report's Application is no view of the block's commit, "" when it is one.
func (j judgement) counts(app observe.Application) string {
	switch {
	case app.RevisionUnread:
		return fmt.Sprintf("the newest node report carries no revision of Argo CD Application %s, as a report of an earlier talos-cluster-kit release does", j.d.Application)
	case app.Revision == "":
		return fmt.Sprintf("Argo CD has compared no revision of Application %s", j.d.Application)
	}
	if why := j.line.holding(app.Revision, j.own); why != "" {
		return fmt.Sprintf("the newest node report shows Argo CD Application %s at %s, %s", j.d.Application, app.Revision, why)
	}
	if app.Objects == nil {
		return fmt.Sprintf("the newest node report carries no list of the objects Argo CD Application %s applied, as a report cut to fit its size does", j.d.Application)
	}
	return ""
}

func (j judgement) judgeObjects(ctx context.Context, app observe.Application) (Verdict, string, error) {
	declared, err := objectsAt(ctx, j.d.Repository, j.own, j.d.Paths)
	if err != nil {
		return VerdictPending, "", err
	}
	listed := make([]*observe.AppliedObject, len(declared))
	for i, o := range declared {
		listed[i] = find(app, o)
	}
	if verdict, why := j.failed(app, declared, listed); verdict == VerdictFailed {
		return verdict, why, nil
	}
	for i, o := range declared {
		if why := notReady(app, o, listed[i]); why != "" {
			return VerdictPending, why, nil
		}
	}
	return j.removedGone(ctx, app, declared)
}

// failed names the first object reading Degraded, or beside a failed sync Argo CD notified, the first not Synced.
func (j judgement) failed(app observe.Application, declared []Object, listed []*observe.AppliedObject) (Verdict, string) {
	for i, o := range declared {
		if listed[i] != nil && listed[i].Health.Status == "Degraded" {
			return VerdictFailed, fmt.Sprintf("%s reads Degraded%s", o, withMessage(listed[i].Health.Message))
		}
	}
	if !j.notice.failedSync() {
		return VerdictPending, ""
	}
	n := j.notice.n
	for i, o := range declared {
		if listed[i] == nil || listed[i].Sync != "Synced" {
			return VerdictFailed, fmt.Sprintf("Argo CD's sync at %s ended in phase %s%s, and %s", n.Revision, n.Phase, withMessage(n.Message), notReady(app, o, listed[i]))
		}
	}
	return VerdictPending, ""
}

// notReady says why an object the block declares keeps its commit pending, "" once it reads Synced and Healthy or with no health check.
func notReady(app observe.Application, o Object, got *observe.AppliedObject) string {
	switch {
	case got == nil:
		return unlisted(app, o)
	case got.Sync != "Synced":
		return fmt.Sprintf("%s reads sync %s", o, got.Sync)
	case got.Health.Status == observe.HealthUnknown:
		return fmt.Sprintf("%s carries no health, as a report of an earlier talos-cluster-kit release does", o)
	case got.Health.Status != "" && got.Health.Status != "Healthy":
		return fmt.Sprintf("%s reads health %s%s", o, got.Health.Status, withMessage(got.Health.Message))
	}
	return ""
}

// removedGone succeeds once no object the block's files declared before its own commit, and no longer do, is among those the Application applied.
func (j judgement) removedGone(ctx context.Context, app observe.Application, declared []Object) (Verdict, string, error) {
	parent, _, err := j.d.Repository.Changed(ctx, j.own)
	if err != nil {
		return VerdictPending, "", fmt.Errorf("gitops: reading the parent of %s: %w", j.own, err)
	}
	before, err := objectsAt(ctx, j.d.Repository, parent, j.d.Paths)
	if err != nil {
		return VerdictPending, "", err
	}
	removed := slices.DeleteFunc(before, func(o Object) bool { return slices.Contains(declared, o) })
	for _, o := range removed {
		if find(app, o) != nil {
			return VerdictPending, fmt.Sprintf("%s, which the commit removed, is still among the objects it applied", o), nil
		}
	}
	return VerdictSucceeded, fmt.Sprintf("each of the %d objects the block's files declare reads Synced, and Healthy or with no health check, and none of the %d it removed is applied", len(declared), len(removed)), nil
}

// unlisted says the Application applied no object o names, and names the namespace it applied one of o's kind and name in when o's manifest names none, since Argo CD lists an object its manifest leaves to the Application's namespace under that namespace.
func unlisted(app observe.Application, o Object) string {
	why := fmt.Sprintf("%s is not among the objects it applied", o)
	if o.Namespace != "" {
		return why
	}
	for _, got := range app.Objects {
		if got.Group == o.Group && got.Kind == o.Kind && got.Name == o.Name && got.Namespace != "" {
			return why + fmt.Sprintf(", though it applied that kind and name in namespace %s, which the block's manifest must name as metadata.namespace", got.Namespace)
		}
	}
	return why
}

func withMessage(message string) string {
	if message == "" {
		return ""
	}
	return ": " + message
}

func find(app observe.Application, o Object) *observe.AppliedObject {
	for i, got := range app.Objects {
		if got.Group == o.Group && got.Kind == o.Kind && got.Namespace == o.Namespace && got.Name == o.Name {
			return &app.Objects[i]
		}
	}
	return nil
}

// objectsAt reads the objects each of paths declares at revision, passing over a path revision does not hold.
func objectsAt(ctx context.Context, repo Repository, revision string, paths []string) ([]Object, error) {
	var objects []Object
	for _, path := range paths {
		content, ok, err := readAt(ctx, repo, revision, path)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		declared, err := Objects(content)
		if err != nil {
			return nil, fmt.Errorf("gitops: reading the objects %s declares at %s: %w", path, revision, err)
		}
		objects = append(objects, declared...)
	}
	return objects, nil
}

// Wait checks the commit every poll until the verdict is no longer pending, and a failed deploy is an outcome, not an error.
func Wait(ctx context.Context, d Deployment, revision string, poll time.Duration) (Outcome, error) {
	if poll <= 0 {
		return Outcome{Verdict: VerdictPending}, errors.New("gitops: the poll interval must be positive")
	}
	last := Outcome{Verdict: VerdictPending}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-timer.C:
		}
		out, err := Check(ctx, d, revision)
		if err != nil || out.Verdict != VerdictPending {
			return out, err
		}
		last = out
		timer.Reset(poll)
	}
}
