package dockerhost

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/events"
)

const (
	defaultGitHubAPI = "https://" + ghapi.APIGitHubHost
	githubWeb        = "https://github.com/"
	githubTimeout    = 30 * time.Second
)

// hostRepo is the host's repository, whose main the host's pull agent applies, one top-level directory per app.
type hostRepo struct {
	client   *ghapi.Client
	fullName string
}

// dialHostRepo refuses, before any request, a host repository outside the GitHub connection's account.
func dialHostRepo(d contract.Desired, recorded string, newClient func(base, token string) *ghapi.Client) (*hostRepo, error) {
	token, owner := d.Secrets["github"]["token"], ghapi.Owner(d)
	if token == "" || owner == "" {
		return nil, handler.Refuse("missing-github-access",
			"the GitHub connection is missing: the request carries no GitHub token or no GitHub owner, so the host's repository cannot be read or written; reconnect GitHub and retry")
	}
	repoURL := d.Bindings[reqHost]["clusterRepoUrl"]
	if repoURL == "" && recorded != "" {
		repoURL = githubWeb + recorded
	}
	full, ok := strings.CutPrefix(repoURL, githubWeb)
	if !ok || !strings.EqualFold(strings.Split(full, "/")[0], owner) {
		return nil, handler.Refuse("host-binding",
			"the host's repository %q is not a repository of the GitHub connection's account %s; subscribe the database to a host of this org", repoURL, owner)
	}
	if _, err := ghapi.SafeRepo(full); err != nil {
		return nil, handler.Refuse("host-binding", "the host's repository %q is not usable: %v", repoURL, err)
	}
	endpoint := d.Instances["github"].Endpoint
	if endpoint == "" {
		endpoint = defaultGitHubAPI
	}
	base, err := ghapi.Base(endpoint, strings.Split(os.Getenv(ghapi.HostsEnv), ",")...)
	if err != nil {
		return nil, handler.Refuse("untrusted-api-base", "github api base rejected: %v", err)
	}
	if newClient == nil {
		newClient = defaultGitHubClient
	}
	return &hostRepo{client: newClient(base.String(), token), fullName: full}, nil
}

func defaultGitHubClient(base, token string) *ghapi.Client {
	return ghapi.NewClient(base, token, &http.Client{Timeout: githubTimeout})
}

// snapshot is main's head and every file path it holds, read in one listing of its tree.
type snapshot struct {
	head  string
	paths map[string]bool
}

func (g *hostRepo) snapshot(ctx context.Context) (snapshot, error) {
	head, err := g.client.HeadCommit(ctx, g.fullName)
	if err != nil {
		return snapshot{}, fmt.Errorf("reading the head of %s: %w", g.fullName, err)
	}
	if head == "" {
		return snapshot{}, fmt.Errorf("the host's repository %s has no main branch yet", g.fullName)
	}
	tree, err := g.client.CommitTree(ctx, g.fullName, head)
	if err != nil {
		return snapshot{}, fmt.Errorf("reading the tree of %s at %s: %w", g.fullName, head, err)
	}
	listed, err := g.client.TreePaths(ctx, g.fullName, tree)
	if err != nil {
		return snapshot{}, fmt.Errorf("listing the files of %s at %s, so the host's apps cannot all be counted: %w", g.fullName, head, err)
	}
	paths := map[string]bool{}
	for _, p := range listed {
		paths[p] = true
	}
	return snapshot{head: head, paths: paths}, nil
}

func (g *hostRepo) read(ctx context.Context, head, path string) (string, error) {
	content, found, err := g.client.ReadFileAt(ctx, g.fullName, head, path)
	if err != nil {
		return "", fmt.Errorf("reading %s in %s: %w", path, g.fullName, err)
	}
	if !found {
		return "", fmt.Errorf("%s in %s is listed at %s but cannot be read", path, g.fullName, head)
	}
	return string(content), nil
}

// commit lands changes on head, an empty content deleting its path, and reports a commit it lands so the host's
// agent pulls it at once.
func (g *hostRepo) commit(ctx context.Context, head, message string, changes map[string]string) error {
	entries := map[string]*string{}
	for path, content := range changes {
		if content == "" {
			entries[path] = nil
			continue
		}
		entries[path] = &content
	}
	landed, err := g.client.CommitChanges(ctx, g.fullName, head, message, entries)
	if err != nil {
		return fmt.Errorf("committing to %s: %w", g.fullName, err)
	}
	// CommitChanges answers head itself when the changes left the tree as it was.
	if landed != head {
		g.report(ctx, landed)
	}
	return nil
}

// report never fails the commit, since the agent's own timed pull takes it anyway.
func (g *hostRepo) report(ctx context.Context, commit string) {
	if err := events.ReportCommit(ctx, g.fullName, commit); err != nil {
		log.Printf("dockerhost: the host's agent takes %s at its next pull, since the platform was not told of it: %v", commit, err)
	}
}
