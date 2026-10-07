package gitops

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/ghapi"
)

// GitHubRepository is the Repository over GitHub's REST API. It reads and
// commits the main branch of Repo, an "owner/name" repository, through
// Client.
type GitHubRepository struct {
	Client *ghapi.Client
	Repo   string
}

// NewGitHubRepository returns the GitHubRepository for repoFullName, taking
// the API base from d's github instance through ghapi.BaseFromEnv and the
// token from d's github secret. No error it returns carries the token.
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
	return &GitHubRepository{Client: ghapi.NewClient(base.String(), token, nil), Repo: repo}, nil
}

// Head returns the commit main points at, or "" when the repository has no
// main yet.
func (r *GitHubRepository) Head(ctx context.Context) (string, error) {
	return r.Client.HeadCommit(ctx, r.Repo)
}

// ReadFile returns path's content at revision and whether it exists there.
// GitHub answers an unknown revision and an absent path alike, so an absent
// path costs one more request to confirm the repository holds revision. A
// revision of "" reports the path absent with no request, and any other
// revision must be a full commit SHA, as ghapi.Client.ReadFileAt requires.
func (r *GitHubRepository) ReadFile(ctx context.Context, revision, path string) ([]byte, bool, error) {
	if revision == "" {
		return nil, false, nil
	}
	content, ok, err := r.Client.ReadFileAt(ctx, r.Repo, revision, path)
	if err != nil || ok {
		return content, ok, err
	}
	if _, err := r.Client.CommitTree(ctx, r.Repo, revision); err != nil {
		return nil, false, fmt.Errorf("gitops: confirming revision %s: %w", revision, err)
	}
	return nil, false, nil
}

// Commit lands changes on parent as one commit on main. A nil content
// deletes its path and any other, empty included, writes it. Content must be
// UTF-8, since the Git Data API carries it as a JSON string. A parent of ""
// creates main, and any other parent must be a full commit SHA. When parent
// is no longer the tip the error wraps ErrStaleBase.
func (r *GitHubRepository) Commit(ctx context.Context, parent string, changes map[string][]byte, message string) (string, error) {
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

// Changed returns revision's first parent and the sorted paths it changed.
// The revision must be a full commit SHA, as
// ghapi.Client.CommitParentAndPaths requires.
func (r *GitHubRepository) Changed(ctx context.Context, revision string) (string, []string, error) {
	return r.Client.CommitParentAndPaths(ctx, r.Repo, revision)
}
