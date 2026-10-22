package gitops

import (
	"context"
	"time"
)

// BlockDeploy is a block's whole desired state for one app.
type BlockDeploy struct {
	App     string
	Files   map[string][]byte
	Message string
	Secrets []string
	Poll    time.Duration
}

// Deployed is the revision Deploy waited on and Argo CD's verdict on it.
type Deployed struct {
	Revision string
	Changed  bool
	Outcome  Outcome
}

// Deploy commits every file once through CommitManifests, then waits for
// Argo CD's verdict on that revision, or on head when nothing changed. A
// failed verdict comes back in Deployed.Outcome, not as an error.
func Deploy(ctx context.Context, repo Repository, src Notifications, b BlockDeploy) (Deployed, error) {
	revision, changed, err := CommitManifests(ctx, repo, b.Files, b.Message, b.Secrets)
	if err != nil {
		return Deployed{}, err
	}
	out := Deployed{Revision: revision, Changed: changed}
	out.Outcome, err = Wait(ctx, src, b.App, revision, b.Poll)
	return out, err
}
