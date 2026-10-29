package landing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
)

// mainState is what main carries of the files a record created.
type mainState struct {
	deleted   bool
	head      string
	unchanged []string
	changed   []string
}

// readMain reads main for each file rec created; a read that fails is an error and never absence.
func readMain(ctx context.Context, c *ghapi.Client, rec Record) (mainState, error) {
	if c.Token == "" {
		return mainState{}, errors.New("no GitHub token was given to read it with")
	}
	deleted, err := repositoryDeleted(ctx, api{c: c, repo: rec.Repository})
	if err != nil || deleted {
		return mainState{deleted: deleted}, err
	}
	head, err := c.HeadCommit(ctx, rec.Repository)
	if err != nil || head == "" {
		return mainState{}, err
	}
	st := mainState{head: head}
	for _, f := range rec.Files {
		if !f.Created {
			continue
		}
		content, held, err := c.ReadFileAt(ctx, rec.Repository, head, f.Path)
		if err != nil {
			return mainState{}, err
		}
		switch {
		case !held:
		case BlobHash(content) == f.Blob:
			st.unchanged = append(st.unchanged, f.Path)
		default:
			st.changed = append(st.changed, f.Path)
		}
	}
	return st, nil
}

// repositoryDeleted proves deletion by two reads with one token: the repository answers 404, and the token's installation covers every repository of its owner, so it would see the repository if it existed.
func repositoryDeleted(ctx context.Context, a api) (bool, error) {
	err := a.repos(ctx, http.MethodGet, "", nil, nil)
	if err == nil {
		return false, nil
	}
	if statusOf(err) != http.StatusNotFound {
		return false, err
	}
	var listing struct {
		Selection    string `json:"repository_selection"`
		Repositories []struct {
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repositories"`
	}
	if err := a.call(ctx, http.MethodGet, "/installation/repositories?per_page=1", nil, &listing); err != nil {
		return false, fmt.Errorf("the repository answers 404 and the installation's repository list cannot be read: %w", err)
	}
	owner, _, _ := strings.Cut(a.repo, "/")
	if listing.Selection == "all" && len(listing.Repositories) > 0 && strings.EqualFold(listing.Repositories[0].Owner.Login, owner) {
		return true, nil
	}
	return false, errors.New("the repository answers 404 and the token's installation does not cover every repository of its owner, so the repository may exist where the token cannot see it")
}
