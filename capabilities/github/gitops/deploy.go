package gitops

import (
	"context"
	"time"
)

type BlockDeploy struct {
	App     string
	Files   map[string][]byte
	Message string
	Secrets []string
	Poll    time.Duration
}

type Deployed struct {
	Revision string
	Changed  bool
	Outcome  Outcome
}

// Deploy waits for Argo CD's verdict on the revision it committed, or on the block's own last commit when nothing changed, settling on a later revision that holds its files, and returns a failed verdict in Outcome rather than as an error.
func Deploy(ctx context.Context, repo Repository, src Notifications, b BlockDeploy) (Deployed, error) {
	revision, changed, err := CommitManifests(ctx, repo, b.Files, b.Message, b.Secrets)
	if err != nil {
		return Deployed{}, err
	}
	out := Deployed{Revision: revision, Changed: changed}
	out.Outcome, err = Wait(ctx, src, repo, b.App, revision, sortedPaths(b.Files), b.Poll)
	return out, err
}
