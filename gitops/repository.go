package gitops

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"

	"github.com/mitstack-eu/operator-kit/contract"
)

// ErrStaleBase is the error a Repository's Commit wraps when the parent it
// was handed is no longer the tip, because another writer landed a commit
// first. CommitManifests and Revert re-read head and retry on it.
var ErrStaleBase = errors.New("gitops: the parent is no longer the tip")

// ErrSecretInManifest is the error CommitManifests wraps when a file it was
// handed carries a secret value. The wrapping error names the file's path
// and never the value.
var ErrSecretInManifest = errors.New("gitops: a manifest carries a secret value")

// maxCommitTries bounds how often a commit re-reads head and retries after
// another writer moved the tip.
const maxCommitTries = 5

// Repository is the GitOps repository a block commits its manifests to, read
// and written by revision. A revision is a commit id, and "" names the empty
// repository, which holds no file.
type Repository interface {
	// Head returns the revision at the tip, or "" when the repository holds
	// no commit yet.
	Head(ctx context.Context) (string, error)

	// ReadFile returns the content of path at revision and whether the path
	// exists there. A revision the repository never held is an error.
	ReadFile(ctx context.Context, revision, path string) ([]byte, bool, error)

	// Commit lands changes on top of parent and returns the new revision. A
	// nil content deletes its path, and every other path carries over from
	// parent. When parent is no longer the tip it lands nothing and returns
	// an error wrapping ErrStaleBase.
	Commit(ctx context.Context, parent string, changes map[string][]byte, message string) (string, error)

	// Changed returns the parent of revision and the sorted paths revision
	// changed relative to it. The parent of the first commit is "". A
	// revision the repository never held is an error.
	Changed(ctx context.Context, revision string) (string, []string, error)
}

// CommitManifests commits files on top of head and returns the revision that
// holds them and whether it committed anything. A nil content removes its
// path. When every file already stands at head with that content it commits
// nothing and returns head.
//
// Before anything is read or written it refuses a file that carries any
// non-empty value of secrets, verbatim or as its standard base64 encoding,
// with an error wrapping ErrSecretInManifest that names the path. A commit
// that finds the tip moved is re-read and retried a bounded number of times,
// after which the error wraps ErrStaleBase.
func CommitManifests(ctx context.Context, repo Repository, files map[string][]byte, message string, secrets []string) (string, bool, error) {
	if err := refuseSecrets(files, secrets); err != nil {
		return "", false, err
	}
	return commitFiles(ctx, repo, files, message)
}

// Revert commits, on top of the current head, each path revision changed
// restored to its content at revision's parent, deleting a path absent
// there. Every other path is left as head holds it. It returns the revision
// that holds the result, which is head unchanged when head already matches
// the parent on every path revision changed.
func Revert(ctx context.Context, repo Repository, revision, message string) (string, error) {
	parent, paths, err := repo.Changed(ctx, revision)
	if err != nil {
		return "", fmt.Errorf("gitops: reading what %s changed: %w", revision, err)
	}
	restore := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, ok, err := readAt(ctx, repo, parent, path)
		if err != nil {
			return "", err
		}
		if !ok {
			content = nil
		}
		restore[path] = content
	}
	rev, _, err := commitFiles(ctx, repo, restore, message)
	return rev, err
}

// SecretValues returns every value d carries in SecretInputs, AppSecrets and
// Secrets, in no particular order, for CommitManifests to refuse. It never
// returns a param value.
func SecretValues(d contract.Desired) []string {
	var values []string
	for _, v := range d.SecretInputs {
		values = append(values, v)
	}
	for _, v := range d.AppSecrets {
		values = append(values, v)
	}
	for _, group := range d.Secrets {
		for _, v := range group {
			values = append(values, v)
		}
	}
	return values
}

// refuseSecrets returns an error wrapping ErrSecretInManifest for the first
// path, in sorted order, whose content carries a non-empty secret verbatim or
// standard base64-encoded.
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

// commitFiles commits the files that differ from head, retrying on a moved
// tip, and returns head unchanged when none differ.
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
			return head, false, nil
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

// changesAt returns the subset of files that revision does not already hold
// as given: a content that differs or is missing, or a nil content whose
// path still exists.
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

// readAt reads path at revision, answering the empty repository's revision
// "" without asking the Repository.
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

// sortedPaths returns the keys of m in sorted order.
func sortedPaths[V any](m map[string]V) []string {
	paths := make([]string, 0, len(m))
	for path := range m {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
