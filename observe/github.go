package observe

import (
	"context"

	"github.com/mitstack-eu/block-kit/contract"
)

type GitHubRepositoryAPI interface {
	RepositoryExists(ctx context.Context, d contract.Desired, fullName string) (bool, error)
}

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

// githubOwned lists by the subscription's topic when the observer's API can, and is otherwise nil.
func githubOwned(api any) TaggedLister {
	if a, ok := api.(GitHubTagAPI); ok {
		return GitHubTagged{API: a, withoutContributions: true}
	}
	return nil
}

type GitHubCommitAPI interface {
	CommitExists(ctx context.Context, d contract.Desired, repo, sha string) (bool, error)
}

// GitHubCommit observes a github.commit by its repo and sha, reporting
// sourceSha and, when the identity holds a branch, sourceBranch; with neither
// while unsubscribing, it takes the newest commit carrying the subscription's
// trailer when its API also implements GitHubTagAPI.
type GitHubCommit struct {
	API GitHubCommitAPI
}

func (o GitHubCommit) heldByTrailer(ctx context.Context, r contract.ResourceRef, noKey contract.Observation) (contract.Observation, error) {
	api, ok := o.API.(GitHubTagAPI)
	if !r.Unsubscribing || !ok || r.Desired.SubscriptionID == "" {
		return noKey, nil
	}
	repo, sha, err := GitHubTagged{API: api}.newestTrailerCommit(ctx, r.Desired, r.Desired.SubscriptionID)
	switch {
	case err != nil:
		return contract.Observation{}, err
	case sha == "":
		return absent(), nil
	}
	return contract.Observation{State: contract.Present, Identity: map[string]string{"repo": repo, "sha": sha}}, nil
}

func (o GitHubCommit) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	ids, obs, ok := identityKeys(r, "repo", "sha")
	if !ok {
		return o.heldByTrailer(ctx, r, obs)
	}
	outputs := map[string]string{"sourceSha": ids[1]}
	if branch := r.Identity["branch"]; branch != "" {
		outputs["sourceBranch"] = branch
	}
	exists, err := o.API.CommitExists(ctx, r.Desired, ids[0], ids[1])
	return existence(r, exists, err, outputs)
}
