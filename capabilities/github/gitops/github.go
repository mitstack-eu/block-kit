package gitops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/reqcache"
)

type GitHubRepository struct {
	Client   *ghapi.Client
	Repo     string
	readOnly bool
}

// No error NewGitHubRepository returns carries the token.
func NewGitHubRepository(d contract.Desired, repoFullName string) (*GitHubRepository, error) {
	repo, err := ghapi.SafeRepo(repoFullName)
	if err != nil {
		return nil, err
	}
	instance, ok := d.Instances["github"]
	if !ok || instance.Endpoint == "" {
		return nil, errors.New("gitops: the request carries no github instance endpoint")
	}
	base, err := ghapi.BaseFromEnv(instance.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("gitops: resolving the github endpoint: %w", err)
	}
	token := d.Secrets["github"]["token"]
	if token == "" {
		return nil, errors.New("gitops: the request carries no github token")
	}
	return &GitHubRepository{Client: ghapi.NewClient(base.String(), token, nil), Repo: repo, readOnly: d.ReadOnly}, nil
}

// Head returns the commit main points at, or "" before main exists.
func (r *GitHubRepository) Head(ctx context.Context) (string, error) {
	return r.Client.HeadCommit(ctx, r.Repo)
}

// commitData keys what a commit holds, which never changes.
type commitData struct {
	base, repo, revision, path string
	changed                    bool
}

// fileRead is what ReadFile answers for one path.
type fileRead struct {
	content []byte
	ok      bool
}

// commitChange is what Changed answers for one revision.
type commitChange struct {
	parent string
	paths  []string
}

// Revision "" holds nothing, and any other must be a full lowercase hex SHA, which ghapi enforces before any request, so a request reads each file of a revision once.
func (r *GitHubRepository) ReadFile(ctx context.Context, revision, path string) ([]byte, bool, error) {
	if revision == "" {
		return nil, false, nil
	}
	got, err := reqcache.Immutable(ctx, commitData{base: r.Client.Base, repo: r.Repo, revision: revision, path: path}, func() (fileRead, error) {
		content, ok, err := r.readFile(ctx, revision, path)
		return fileRead{content, ok}, err
	})
	return slices.Clone(got.content), got.ok, err
}

func (r *GitHubRepository) readFile(ctx context.Context, revision, path string) ([]byte, bool, error) {
	content, ok, err := r.Client.ReadFileAt(ctx, r.Repo, revision, path)
	if err != nil || ok {
		return content, ok, err
	}
	if _, err := r.Client.CommitTree(ctx, r.Repo, revision); err != nil {
		return nil, false, fmt.Errorf("gitops: confirming revision %s: %w", revision, err)
	}
	return nil, false, nil
}

// A parent no longer the tip yields an error wrapping ErrStaleBase, and a repository built from a read-only request lands nothing and yields one wrapping contract.ErrReadOnly.
func (r *GitHubRepository) Commit(ctx context.Context, parent string, changes map[string][]byte, message string) (string, error) {
	if r.readOnly {
		return "", fmt.Errorf("gitops: committing to %s: %w", r.Repo, contract.ErrReadOnly)
	}
	entries := make(map[string]*string, len(changes))
	for path, content := range changes {
		if content == nil {
			entries[path] = nil
			continue
		}
		if !utf8.Valid(content) {
			return "", fmt.Errorf("gitops: %s is not UTF-8 text", path)
		}
		s := string(content)
		entries[path] = &s
	}
	rev, err := r.Client.CommitChanges(ctx, r.Repo, parent, message, entries)
	if errors.Is(err, ghapi.ErrNotFastForward) {
		return "", fmt.Errorf("%w: %w", ErrStaleBase, err)
	}
	return rev, err
}

// Revision must be a full lowercase hex SHA, and a request reads each revision once.
func (r *GitHubRepository) Changed(ctx context.Context, revision string) (string, []string, error) {
	got, err := reqcache.Immutable(ctx, commitData{base: r.Client.Base, repo: r.Repo, revision: revision, changed: true}, func() (commitChange, error) {
		parent, paths, err := r.Client.CommitParentAndPaths(ctx, r.Repo, revision)
		return commitChange{parent, paths}, err
	})
	return got.parent, slices.Clone(got.paths), err
}
