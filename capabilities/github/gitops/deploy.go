package gitops

import (
	"context"
	"time"
)

// BlockDeploy is the files a deploy commits, under its message, refusing any of Secrets, with Wait's poll interval.
type BlockDeploy struct {
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

// Deploy commits b and waits for the verdict on the block's own commit over the paths b.Files names, returning a failed verdict in Outcome rather than as an error.
func Deploy(ctx context.Context, d Deployment, b BlockDeploy) (Deployed, error) {
	revision, changed, err := CommitManifests(ctx, d.Repository, b.Files, b.Message, b.Secrets)
	if err != nil {
		return Deployed{}, err
	}
	out := Deployed{Revision: revision, Changed: changed}
	d.Paths = sortedPaths(b.Files)
	out.Outcome, err = Wait(ctx, d, revision, b.Poll)
	return out, err
}
