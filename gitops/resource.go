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

const (
	refusedSecretInManifest    = "secret-in-manifest"
	refusedInvalidManifestPath = "invalid-manifest-path"
)

const (
	identityApp      = "app"
	identityRevision = "revision"
	identityPaths    = "paths"
)

// Rendered is one resource's Argo CD app, its files by repository path, each
// with content, and the outputs it publishes, none of them a secret.
type Rendered struct {
	App     string
	Files   map[string][]byte
	Outputs map[string]string
}

// Resource serves one declared resource through GitOps, as a
// handler.DeclaredResource the Handler drives per call. All three fields are
// required.
type Resource struct {
	// Repository returns the GitOps repository the resource for d lives in.
	Repository func(ctx context.Context, d contract.Desired) (Repository, error)

	// Notifications returns d's notification source, usually
	// NotificationsFromContext.
	Notifications func(ctx context.Context, d contract.Desired) (Notifications, error)

	// Render renders d's files. A bare Observe or Delete hands it a Desired
	// carrying the Identity alone.
	Render func(d contract.Desired) (Rendered, error)
}

// Ensure commits the rendered files the way EnsureWithReport does.
func (r *Resource) Ensure(ctx context.Context, d contract.Desired) error {
	_, err := r.EnsureWithReport(ctx, d)
	return err
}

// EnsureWithReport commits the rendered files through CommitManifests and
// reports the app, the revision and the comma-joined sorted paths. When the
// recorded identity is of the render's own app, the same commit removes each
// recorded path the render no longer has; another app's record removes
// nothing. A file carrying a secret is refused as secret-in-manifest, and a
// path holding a comma as invalid-manifest-path, before anything is written.
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
	changes := make(map[string][]byte, len(out.Files))
	for path, content := range out.Files {
		changes[path] = content
	}
	for _, path := range stalePaths(d, out) {
		changes[path] = nil
	}
	rev, changed, err := CommitManifests(ctx, repo, changes, out.App+": update manifests", SecretValues(d))
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

// ObserveWithRequest judges the app's newest notification, whatever its
// revision, once the files at that revision stand as they do at head, since
// Argo CD may sync a later revision than the one committed. A live sync on a
// scratch cluster with two commits inside one poll interval would show
// whether it does. A block reads only the notifications of the cluster its
// kubernetes-cluster requirement binds, so binding none or several stays
// unknown, and a cluster hosting several orgs' apps needs its own rule.
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

// DeleteWithRequest commits the removal of the recorded paths, or of the
// rendered ones when none are recorded.
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

func (r *Resource) deletePaths(d contract.Desired) (string, []string, error) {
	paths := recordedPaths(d)
	if len(paths) > 0 {
		return d.ResourceIdentity[identityApp], paths, nil
	}
	out, err := r.render(d)
	if err != nil {
		return "", nil, err
	}
	return out.App, sortedPaths(out.Files), nil
}

func recordedPaths(d contract.Desired) []string {
	var paths []string
	for _, path := range strings.Split(d.ResourceIdentity[identityPaths], ",") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func stalePaths(d contract.Desired, out Rendered) []string {
	if d.ResourceIdentity[identityApp] != out.App {
		return nil
	}
	var stale []string
	for _, path := range recordedPaths(d) {
		if _, rendered := out.Files[path]; !rendered {
			stale = append(stale, path)
		}
	}
	return stale
}

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

func (r *Resource) repository(ctx context.Context, d contract.Desired) (Repository, error) {
	repo, err := r.Repository(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("gitops: reaching the repository: %w", err)
	}
	return repo, nil
}

// reportedRevision keeps an unchanged ensure reporting its recorded revision
// while that revision still holds every file.
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

// standing is how a set of files stands at one revision.
type standing struct {
	total   int
	missing []string
	differ  []string
}

func (s standing) all() bool { return len(s.missing) == 0 && len(s.differ) == 0 }

func (s standing) none() bool { return len(s.missing) == s.total }

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

func unknown(format string, args ...any) contract.Observation {
	return contract.Observation{State: contract.Unknown, StatusDetail: fmt.Sprintf(format, args...)}
}
