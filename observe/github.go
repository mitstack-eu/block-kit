package observe

import (
	"context"

	"github.com/mitstack-eu/block-kit/contract"
)

type GitHubRepositoryAPI interface {
	RepositoryExists(ctx context.Context, d contract.Desired, fullName string) (bool, error)
}

// GitHubRepository observes a github.repository by its repo. While unsubscribing with no repo, it lists the repositories carrying the subscription's topic and answers from that.
type GitHubRepository struct {
	API GitHubRepositoryAPI
}

func (o GitHubRepository) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	ids, obs, ok := identityKeys(r, "repo")
	if !ok {
		return heldByOwner(ctx, r, githubOwned(o.API), "github.repository", "repo", obs)
	}
	exists, err := o.API.RepositoryExists(ctx, r.Desired, ids[0])
	return existence(r, exists, err, map[string]string{"repo": ids[0]})
}

// githubOwned lists by the subscription's topic when the observer's API can.
// Otherwise it is nil, and the observer answers unknown as it did before.
func githubOwned(api any) TaggedLister {
	if a, ok := api.(GitHubTagAPI); ok {
		return GitHubTagged{API: a}
	}
	return nil
}

type GitHubCommitAPI interface {
	CommitExists(ctx context.Context, d contract.Desired, repo, sha string) (bool, error)
}

// GitHubCommit observes a github.commit by its repo and sha, reporting
// sourceSha and, when the identity holds a branch, sourceBranch.
type GitHubCommit struct {
	API GitHubCommitAPI
}

func (o GitHubCommit) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	ids, obs, ok := identityKeys(r, "repo", "sha")
	if !ok {
		return obs, nil
	}
	outputs := map[string]string{"sourceSha": ids[1]}
	if branch := r.Identity["branch"]; branch != "" {
		outputs["sourceBranch"] = branch
	}
	exists, err := o.API.CommitExists(ctx, r.Desired, ids[0], ids[1])
	return existence(r, exists, err, outputs)
}
