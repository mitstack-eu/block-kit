package gitops

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/mitstack-eu/block-kit/contract"
)

// ErrStaleBase is wrapped by a Repository's Commit when parent is no longer the tip.
var ErrStaleBase = errors.New("gitops: the parent is no longer the tip")

// ErrSecretInManifest is wrapped by CommitManifests, naming the path but never the value.
var ErrSecretInManifest = errors.New("gitops: a manifest carries a secret value")

const maxCommitTries = 5

// Repository is a GitOps repository addressed by commit id, where "" names the empty repository.
type Repository interface {
	// Head returns the tip, or "" when there is no commit yet.
	Head(ctx context.Context) (string, error)

	// ReadFile returns path's content at revision and whether it exists.
	ReadFile(ctx context.Context, revision, path string) ([]byte, bool, error)

	// Commit lands changes on parent, nil deleting a path, and lands nothing but wraps ErrStaleBase when parent is not the tip.
	Commit(ctx context.Context, parent string, changes map[string][]byte, message string) (string, error)

	// Changed returns revision's parent, "" for the first commit, and the sorted paths it changed.
	Changed(ctx context.Context, revision string) (string, []string, error)
}

// CommitManifests refuses a file carrying a secret, then commits the files that differ from head, retrying a moved tip a bounded number of times, and returns the block's own last commit when none differ.
// The guard matches a value only verbatim or as its own standard base64, so a block must never render secret material in any form.
func CommitManifests(ctx context.Context, repo Repository, files map[string][]byte, message string, secrets []string) (string, bool, error) {
	if err := refuseSecrets(files, secrets); err != nil {
		return "", false, err
	}
	return commitFiles(ctx, repo, files, message)
}

// ErrHeadMoved is wrapped by Revert when a commit after the failed revision changed a file the block owns, since that commit waits for its own verdict.
var ErrHeadMoved = errors.New("gitops: a commit after the failed revision changed the block's files")

// maxRevertWalk bounds how many commits Revert reads back from head, and from the failed revision.
const maxRevertWalk = 50

// Owned returns the paths a block owns at revision, "" being the empty repository.
type Owned func(ctx context.Context, revision string) ([]string, error)

// BlockRevert names the Argo CD app whose notifications judge each revision, the revision it failed, and the files the block owns.
type BlockRevert struct {
	App     string
	Failed  string
	Owned   Owned
	Message string
}

// Reverted is the revert's revision, head itself when nothing differed, and the healthy revision whose files it restored, "" when it removed them.
type Reverted struct {
	Revision string
	Healthy  string
}

// Revert restores, on head, every file the block owns to its content at the newest ancestor of the failed revision whose newest notification succeeds, or removes them when the failed revision is the block's first commit.
func Revert(ctx context.Context, repo Repository, src Notifications, b BlockRevert) (Reverted, error) {
	if b.App == "" || b.Failed == "" || b.Owned == nil {
		return Reverted{}, errors.New("gitops: a revert needs the app, the failed revision and the files the block owns")
	}
	var err error
	for range maxCommitTries {
		var out Reverted
		out, err = revertOnHead(ctx, repo, src, b)
		if !errors.Is(err, ErrStaleBase) {
			return out, err
		}
	}
	return Reverted{}, fmt.Errorf("gitops: the tip moved on each of %d tries: %w", maxCommitTries, err)
}

func revertOnHead(ctx context.Context, repo Repository, src Notifications, b BlockRevert) (Reverted, error) {
	head, err := repo.Head(ctx)
	if err != nil {
		return Reverted{}, fmt.Errorf("gitops: reading head: %w", err)
	}
	ownedAtHead, err := ownedAt(ctx, b, head)
	if err != nil {
		return Reverted{}, err
	}
	ownedAtFailed, err := ownedAt(ctx, b, b.Failed)
	if err != nil {
		return Reverted{}, err
	}
	if err := refuseMovedHead(ctx, repo, head, b.Failed, slices.Concat(ownedAtFailed, ownedAtHead)); err != nil {
		return Reverted{}, err
	}
	base, err := findBaseline(ctx, repo, src, b)
	if err != nil {
		return Reverted{}, err
	}
	restore, err := restoreFrom(ctx, repo, b, base, ownedAtHead)
	if err != nil {
		return Reverted{}, err
	}
	changes, err := changesAt(ctx, repo, head, restore)
	if err != nil {
		return Reverted{}, err
	}
	out := Reverted{Revision: head, Healthy: base.healthy}
	if len(changes) == 0 {
		return out, nil
	}
	if out.Revision, err = repo.Commit(ctx, head, changes, b.Message); err != nil {
		return Reverted{}, fmt.Errorf("gitops: committing the revert of %s: %w", b.Failed, err)
	}
	return out, nil
}

// refuseMovedHead walks from head back to failed and refuses a commit on the way that changed a file the block owns at either end.
func refuseMovedHead(ctx context.Context, repo Repository, head, failed string, owned []string) error {
	var moved error
	revision := head
	for range maxRevertWalk {
		if revision == failed {
			return moved
		}
		if revision == "" {
			return fmt.Errorf("gitops: %s is not an ancestor of head %s", failed, head)
		}
		parent, paths, err := repo.Changed(ctx, revision)
		if err != nil {
			return fmt.Errorf("gitops: reading what %s changed: %w", revision, err)
		}
		for _, path := range paths {
			if moved == nil && slices.Contains(owned, path) {
				moved = fmt.Errorf("%w: %s changed %s after %s", ErrHeadMoved, revision, path, failed)
			}
		}
		revision = parent
	}
	return fmt.Errorf("gitops: %s is not within %d commits of head %s", failed, maxRevertWalk, head)
}

// baseline is the revision whose files a revert restores, the healthy revision it is or "" before the block's first commit, and every path changed after it up to the failed revision.
type baseline struct {
	revision string
	healthy  string
	changed  []string
}

func findBaseline(ctx context.Context, repo Repository, src Notifications, b BlockRevert) (baseline, error) {
	var changed []string
	revision := b.Failed
	for range maxRevertWalk {
		parent, paths, err := repo.Changed(ctx, revision)
		if err != nil {
			return baseline{}, fmt.Errorf("gitops: reading what %s changed: %w", revision, err)
		}
		changed = append(changed, paths...)
		healthy, err := judgedHealthy(ctx, src, b.App, parent)
		if err != nil {
			return baseline{}, err
		}
		if healthy {
			return baseline{revision: parent, healthy: parent, changed: changed}, nil
		}
		first, err := holdsNone(ctx, repo, b, parent)
		if err != nil {
			return baseline{}, err
		}
		if first {
			return beforeFirstCommit(b, revision, parent, changed)
		}
		revision = parent
	}
	return baseline{}, fmt.Errorf("gitops: Argo CD judged none of the %d commits before %s healthy, and none of them is the block's first", maxRevertWalk, b.Failed)
}

// judgedHealthy reads whether Argo CD notified a sync of revision that succeeded with the whole Application Healthy, the one sign of a healthy earlier revision the platform keeps.
func judgedHealthy(ctx context.Context, src Notifications, app, revision string) (bool, error) {
	if revision == "" {
		return false, nil
	}
	n, ok, err := src.Latest(ctx, app, revision)
	if err != nil {
		return false, fmt.Errorf("gitops: reading the notification for %s at %s: %w", app, revision, err)
	}
	return ok && n.Revision == revision && n.Phase == "Succeeded" && n.Health == "Healthy", nil
}

// beforeFirstCommit removes the block's files only when the failed revision is its first commit, since the platform answers only a revision's newest notification and keeps only an app's newest, so an earlier revision may have run healthy unseen.
func beforeFirstCommit(b BlockRevert, first, parent string, changed []string) (baseline, error) {
	if first != b.Failed {
		return baseline{}, fmt.Errorf("gitops: Argo CD's newest notification of no revision of %s from its first commit %s to %s reads healthy, but one may have run healthy before, so the revert removes nothing", b.App, first, b.Failed)
	}
	return baseline{revision: parent, changed: changed}, nil
}

func holdsNone(ctx context.Context, repo Repository, b BlockRevert, revision string) (bool, error) {
	owned, err := ownedAt(ctx, b, revision)
	if err != nil {
		return false, err
	}
	for _, path := range owned {
		_, ok, err := readAt(ctx, repo, revision, path)
		if err != nil || ok {
			return false, err
		}
	}
	return true, nil
}

// restoreFrom is the content at the baseline of each path changed since that the block owns at head or there, nil where the baseline lacks it.
func restoreFrom(ctx context.Context, repo Repository, b BlockRevert, base baseline, ownedAtHead []string) (map[string][]byte, error) {
	ownedThen, err := ownedAt(ctx, b, base.revision)
	if err != nil {
		return nil, err
	}
	restore := map[string][]byte{}
	for _, path := range base.changed {
		_, done := restore[path]
		if done || !(slices.Contains(ownedAtHead, path) || slices.Contains(ownedThen, path)) {
			continue
		}
		content, ok, err := readAt(ctx, repo, base.revision, path)
		if err != nil {
			return nil, err
		}
		if !ok {
			content = nil
		}
		restore[path] = content
	}
	return restore, nil
}

func ownedAt(ctx context.Context, b BlockRevert, revision string) ([]string, error) {
	owned, err := b.Owned(ctx, revision)
	if err != nil {
		return nil, fmt.Errorf("gitops: reading the files the block owns at %q: %w", revision, err)
	}
	return owned, nil
}

type secretGroupKey struct {
	group string
	key   string
}

// secretGroupIdentifiers are identifiers, not credentials, so a manifest may carry them, as a repository URL carries the github owner.
var secretGroupIdentifiers = map[secretGroupKey]bool{
	{group: "github", key: "owner"}: true,
}

// SecretValues returns every value in d's SecretInputs, AppSecrets and Secrets except secretGroupIdentifiers, for CommitManifests to refuse.
func SecretValues(d contract.Desired) []string {
	var values []string
	for _, v := range d.SecretInputs {
		values = append(values, v)
	}
	for _, v := range d.AppSecrets {
		values = append(values, v)
	}
	for groupName, group := range d.Secrets {
		for key, v := range group {
			if secretGroupIdentifiers[secretGroupKey{group: groupName, key: key}] {
				continue
			}
			values = append(values, v)
		}
	}
	return values
}

func refuseSecrets(files map[string][]byte, secrets []string) error {
	var needles [][]byte
	for _, s := range secrets {
		if s == "" {
			continue
		}
		needles = append(needles, []byte(s), []byte(base64.StdEncoding.EncodeToString([]byte(s))))
	}
	for _, path := range sortedPaths(files) {
		for _, needle := range needles {
			if bytes.Contains(files[path], needle) {
				return fmt.Errorf("%w: %s", ErrSecretInManifest, path)
			}
		}
	}
	return nil
}

func commitFiles(ctx context.Context, repo Repository, files map[string][]byte, message string) (string, bool, error) {
	var err error
	for range maxCommitTries {
		var head string
		head, err = repo.Head(ctx)
		if err != nil {
			return "", false, fmt.Errorf("gitops: reading head: %w", err)
		}
		changes, cerr := changesAt(ctx, repo, head, files)
		if cerr != nil {
			return "", false, cerr
		}
		if len(changes) == 0 {
			own, err := LastChanged(ctx, repo, head, sortedPaths(files))
			return own, false, err
		}
		var rev string
		rev, err = repo.Commit(ctx, head, changes, message)
		if err == nil {
			return rev, true, nil
		}
		if !errors.Is(err, ErrStaleBase) {
			return "", false, fmt.Errorf("gitops: committing: %w", err)
		}
	}
	return "", false, fmt.Errorf("gitops: the tip moved on each of %d tries: %w", maxCommitTries, err)
}

// maxLineageWalk bounds how many commits CommitManifests and Check read back from head.
const maxLineageWalk = 50

// lineage is the first-parent chain from head down to the block's own commit, head first, with the block's paths each commit changed.
type lineage struct {
	commits []string
	owned   [][]string
	// own indexes the block's own commit, -1 when the walk never reached the revision it started from.
	own int
}

// walkLineage reads back from head to the newest commit at or before revision that changed one of paths, or to the oldest commit within the bound when none did, since head then holds the block's files as that commit does.
func walkLineage(ctx context.Context, repo Repository, head, revision string, paths []string) (lineage, error) {
	l := lineage{own: -1}
	reached := false
	for rev := head; rev != "" && len(l.commits) < maxLineageWalk; {
		parent, changed, err := repo.Changed(ctx, rev)
		if err != nil {
			return lineage{}, fmt.Errorf("gitops: reading what %s changed: %w", rev, err)
		}
		var owned []string
		for _, path := range changed {
			if slices.Contains(paths, path) {
				owned = append(owned, path)
			}
		}
		l.commits, l.owned = append(l.commits, rev), append(l.owned, owned)
		reached = reached || rev == revision
		if reached && len(owned) > 0 {
			l.own = len(l.commits) - 1
			return l, nil
		}
		rev = parent
	}
	if reached {
		l.own = len(l.commits) - 1
	}
	return l, nil
}

// ownRevision is the block's own commit, or revision itself when the walk never reached it.
func (l lineage) ownRevision(revision string) string {
	if l.own < 0 {
		return revision
	}
	return l.commits[l.own]
}

// at indexes revision when it is the block's own commit or a later one, and is -1 otherwise.
func (l lineage) at(revision string) int {
	if l.own < 0 {
		return -1
	}
	return slices.Index(l.commits[:l.own+1], revision)
}

// holding says why revision does not hold the block's files as its own commit own does, "" when it is that commit or a later one in which no commit changed them.
func (l lineage) holding(revision, own string) string {
	if revision == own {
		return ""
	}
	i := l.at(revision)
	if i < 0 {
		return fmt.Sprintf("which does not hold the block's commit %s", own)
	}
	if moved, path := l.changedAfterOwn(i); moved != "" {
		return fmt.Sprintf("but commit %s after the block's own %s changed %s", moved, own, path)
	}
	return ""
}

// changedAfterOwn names the oldest commit after the block's own, up to the one at index i, that changed one of its paths, and the first such path.
func (l lineage) changedAfterOwn(i int) (string, string) {
	for j := l.own - 1; j >= i; j-- {
		if len(l.owned[j]) > 0 {
			return l.commits[j], l.owned[j][0]
		}
	}
	return "", ""
}

// LastChanged returns the block's own commit, the newest at or before revision that changed one of paths, or the oldest within the bound when none did.
func LastChanged(ctx context.Context, repo Repository, revision string, paths []string) (string, error) {
	l, err := walkLineage(ctx, repo, revision, revision, paths)
	if err != nil {
		return "", err
	}
	return l.ownRevision(revision), nil
}

func changesAt(ctx context.Context, repo Repository, revision string, files map[string][]byte) (map[string][]byte, error) {
	changes := map[string][]byte{}
	for path, want := range files {
		got, ok, err := readAt(ctx, repo, revision, path)
		if err != nil {
			return nil, err
		}
		switch {
		case want == nil && ok:
			changes[path] = nil
		case want != nil && (!ok || !bytes.Equal(got, want)):
			changes[path] = want
		}
	}
	return changes, nil
}

func readAt(ctx context.Context, repo Repository, revision, path string) ([]byte, bool, error) {
	if revision == "" {
		return nil, false, nil
	}
	content, ok, err := repo.ReadFile(ctx, revision, path)
	if err != nil {
		return nil, false, fmt.Errorf("gitops: reading %s at %s: %w", path, revision, err)
	}
	return content, ok, nil
}

func sortedPaths[V any](m map[string]V) []string {
	paths := make([]string, 0, len(m))
	for path := range m {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
