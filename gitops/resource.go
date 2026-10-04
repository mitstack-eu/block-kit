package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
)

// The refusal classes EnsureWithReport returns as a *handler.Refused.
const (
	// refusedSecretInManifest refuses a rendered file carrying a secret.
	refusedSecretInManifest = "secret-in-manifest"
	// refusedInvalidManifestPath refuses a rendered path the identity's
	// comma-joined paths list cannot carry.
	refusedInvalidManifestPath = "invalid-manifest-path"
)

// The keys of the identity EnsureWithReport reports.
const (
	identityApp      = "app"
	identityRevision = "revision"
	identityPaths    = "paths"
)

// Rendered is what a block renders for one declared resource: the Argo CD
// application its files deploy through, the files keyed by their path in the
// repository, and the outputs the resource publishes once deployed. Every
// file has content, and no output may carry a secret.
type Rendered struct {
	App     string
	Files   map[string][]byte
	Outputs map[string]string
}

// Resource serves one declared resource by committing its rendered
// manifests to a GitOps repository and judging the deploy from Argo CD's
// notification. It implements handler.DeclaredResource,
// handler.ReportingResource, handler.RequestObserver and
// handler.RequestDeleter, so the Handler drives it per call with that call's
// own Desired. All three fields are required.
type Resource struct {
	// Repository returns the GitOps repository the resource for d lives in.
	Repository func(ctx context.Context, d contract.Desired) (Repository, error)

	// Notifications returns the source of Argo CD's notifications for d,
	// usually NotificationsFromContext.
	Notifications func(ctx context.Context, d contract.Desired) (Notifications, error)

	// Render renders the resource's files from d. A bare Observe or Delete
	// hands it a Desired carrying the Identity alone, so whatever it needs
	// beyond that must come from the inputs and identity a request records.
	Render func(d contract.Desired) (Rendered, error)
}

// Ensure commits the rendered files the way EnsureWithReport does.
func (r *Resource) Ensure(ctx context.Context, d contract.Desired) error {
	_, err := r.EnsureWithReport(ctx, d)
	return err
}

// EnsureWithReport renders d and commits the files through CommitManifests,
// refusing every secret d carries. It reports the identity app, revision and
// paths, the last being the sorted paths joined by commas, and the rendered
// outputs.
//
// A rendered file carrying a secret is refused with class
// secret-in-manifest, and a path holding a comma with class
// invalid-manifest-path, both before anything is written. When nothing
// changed it reports the revision d.ResourceIdentity records, provided the
// files still stand there, and head otherwise.
func (r *Resource) EnsureWithReport(ctx context.Context, d contract.Desired) (contract.Ensured, error) {
	out, err := r.render(d)
	if err != nil {
		return contract.Ensured{}, err
	}
	paths := sortedPaths(out.Files)
	for _, path := range paths {
		if strings.Contains(path, ",") {
			return contract.Ensured{}, handler.Refuse(refusedInvalidManifestPath,
				"the rendered path %s holds a comma, which the identity's paths list cannot carry", path)
		}
	}
	repo, err := r.repository(ctx, d)
	if err != nil {
		return contract.Ensured{}, err
	}
	rev, changed, err := CommitManifests(ctx, repo, out.Files, out.App+": update manifests", SecretValues(d))
	if errors.Is(err, ErrSecretInManifest) {
		return contract.Ensured{}, handler.Refuse(refusedSecretInManifest, "%v", err)
	}
	if err != nil {
		return contract.Ensured{}, err
	}
	if !changed {
		rev = reportedRevision(ctx, repo, d.ResourceIdentity[identityRevision], rev, out.Files)
	}
	return contract.Ensured{
		Identity: map[string]string{
			identityApp:      out.App,
			identityRevision: rev,
			identityPaths:    strings.Join(paths, ","),
		},
		Outputs: out.Outputs,
	}, nil
}

// Observe observes the resource with a Desired carrying id alone.
func (r *Resource) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	return r.ObserveWithRequest(ctx, contract.Desired{Identity: id})
}

// ObserveWithRequest reports whether the rendered files are deployed.
//
// It checks head first. An empty repository is absent with no notification
// read. Files that stand at head only in part, or with other content, are
// unknown naming each such path. Otherwise head holds every file, or none,
// and it reads the app's newest notification, whatever its revision.
//
// No notification yet is unknown. A notification whose revision does not
// hold the files the way head does is unknown, because Argo CD has not yet
// reported a revision carrying the change. Otherwise the notification judges
// the resource: succeeded is present with the outputs, or absent; pending is
// unknown; failed is an error carrying the phase, the health and the
// message. It never reads the revision d.ResourceIdentity records.
func (r *Resource) ObserveWithRequest(ctx context.Context, d contract.Desired) (contract.Observation, error) {
	out, err := r.render(d)
	if err != nil {
		return contract.Observation{}, err
	}
	repo, err := r.repository(ctx, d)
	if err != nil {
		return contract.Observation{}, err
	}
	head, err := repo.Head(ctx)
	if err != nil {
		return contract.Observation{}, fmt.Errorf("gitops: reading head: %w", err)
	}
	if head == "" {
		return contract.Observation{State: contract.Absent}, nil
	}
	atHead, err := compareAt(ctx, repo, head, out.Files)
	if err != nil {
		return contract.Observation{}, err
	}
	var want contract.State
	switch {
	case atHead.all():
		want = contract.Present
	case atHead.none():
		want = contract.Absent
	default:
		return unknown("head %s holds the rendered manifests only in part: %s", head, atHead.describe()), nil
	}

	src, err := r.Notifications(ctx, d)
	if err != nil {
		return contract.Observation{}, fmt.Errorf("gitops: reaching the notifications for %s: %w", out.App, err)
	}
	n, ok, err := src.Latest(ctx, out.App, "")
	if err != nil {
		return contract.Observation{}, fmt.Errorf("gitops: reading the newest notification for %s: %w", out.App, err)
	}
	if !ok {
		return unknown("Argo CD has reported no deploy of %s yet", out.App), nil
	}
	atNotified, err := compareAt(ctx, repo, n.Revision, out.Files)
	if err != nil {
		return contract.Observation{}, err
	}
	if want == contract.Present && !atNotified.all() {
		return unknown("Argo CD last reported %s at %s, which does not yet carry the change at head %s", out.App, n.Revision, head), nil
	}
	if want == contract.Absent && !atNotified.none() {
		return unknown("Argo CD last reported %s at %s, which does not yet carry the removal at head %s", out.App, n.Revision, head), nil
	}

	switch Judge(n, n.Revision) {
	case VerdictSucceeded:
		if want == contract.Present {
			return contract.Observation{State: contract.Present, Outputs: out.Outputs}, nil
		}
		return contract.Observation{State: contract.Absent}, nil
	case VerdictFailed:
		return contract.Observation{}, fmt.Errorf("gitops: Argo CD reports the deploy of %s at %s failed: phase %s, health %s: %s",
			out.App, n.Revision, n.Phase, n.Health, n.Message)
	default:
		return unknown("Argo CD reports %s at %s with phase %q and health %q", out.App, n.Revision, n.Phase, n.Health), nil
	}
}

// Delete deletes the resource with a Desired carrying id alone.
func (r *Resource) Delete(ctx context.Context, id contract.Identity) error {
	return r.DeleteWithRequest(ctx, contract.Desired{Identity: id})
}

// DeleteWithRequest commits the removal of the paths d.ResourceIdentity
// records, without rendering, or of the rendered paths when none are
// recorded. Paths already gone commit nothing, so deleting a resource that
// was never created succeeds.
func (r *Resource) DeleteWithRequest(ctx context.Context, d contract.Desired) error {
	app, paths, err := r.deletePaths(d)
	if err != nil {
		return err
	}
	repo, err := r.repository(ctx, d)
	if err != nil {
		return err
	}
	removal := make(map[string][]byte, len(paths))
	for _, path := range paths {
		removal[path] = nil
	}
	_, _, err = commitFiles(ctx, repo, removal, app+": remove manifests")
	return err
}

// deletePaths returns the app and the paths a delete removes: those d's
// recorded identity names, or the rendered ones when it names none.
func (r *Resource) deletePaths(d contract.Desired) (string, []string, error) {
	var paths []string
	for _, path := range strings.Split(d.ResourceIdentity[identityPaths], ",") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) > 0 {
		return d.ResourceIdentity[identityApp], paths, nil
	}
	out, err := r.render(d)
	if err != nil {
		return "", nil, err
	}
	return out.App, sortedPaths(out.Files), nil
}

// render calls Render and refuses a render naming no app or no file, or a
// file with no content.
func (r *Resource) render(d contract.Desired) (Rendered, error) {
	out, err := r.Render(d)
	if err != nil {
		return Rendered{}, fmt.Errorf("gitops: rendering: %w", err)
	}
	if out.App == "" {
		return Rendered{}, errors.New("gitops: the render names no app")
	}
	if len(out.Files) == 0 {
		return Rendered{}, fmt.Errorf("gitops: the render for %s holds no file", out.App)
	}
	for _, path := range sortedPaths(out.Files) {
		if out.Files[path] == nil {
			return Rendered{}, fmt.Errorf("gitops: the render for %s gives %s no content", out.App, path)
		}
	}
	return out, nil
}

// repository calls Repository, naming what failed.
func (r *Resource) repository(ctx context.Context, d contract.Desired) (Repository, error) {
	repo, err := r.Repository(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("gitops: reaching the repository: %w", err)
	}
	return repo, nil
}

// reportedRevision returns recorded when it still holds every file, and head
// otherwise, including when recorded cannot be read. An unchanged ensure
// replayed from its record then reports the revision it reported before.
func reportedRevision(ctx context.Context, repo Repository, recorded, head string, files map[string][]byte) string {
	if recorded == "" || recorded == head {
		return head
	}
	at, err := compareAt(ctx, repo, recorded, files)
	if err != nil || !at.all() {
		return head
	}
	return recorded
}

// standing is how a set of files stands at one revision: which paths are
// missing there and which hold other content.
type standing struct {
	total   int
	missing []string
	differ  []string
}

// all reports whether every file stands with its content.
func (s standing) all() bool { return len(s.missing) == 0 && len(s.differ) == 0 }

// none reports whether every file is missing.
func (s standing) none() bool { return len(s.missing) == s.total }

// describe names each path that does not stand with its content.
func (s standing) describe() string {
	var parts []string
	for _, path := range s.missing {
		parts = append(parts, path+" is missing")
	}
	for _, path := range s.differ {
		parts = append(parts, path+" differs")
	}
	return strings.Join(parts, ", ")
}

// compareAt reports how files stand at revision.
func compareAt(ctx context.Context, repo Repository, revision string, files map[string][]byte) (standing, error) {
	s := standing{total: len(files)}
	for _, path := range sortedPaths(files) {
		got, ok, err := readAt(ctx, repo, revision, path)
		if err != nil {
			return standing{}, err
		}
		switch {
		case !ok:
			s.missing = append(s.missing, path)
		case !bytes.Equal(got, files[path]):
			s.differ = append(s.differ, path)
		}
	}
	return s, nil
}

// unknown is an Unknown observation with a formatted detail.
func unknown(format string, args ...any) contract.Observation {
	return contract.Observation{State: contract.Unknown, StatusDetail: fmt.Sprintf(format, args...)}
}
