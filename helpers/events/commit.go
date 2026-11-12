package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const gitopsCommitsPath = "/v1/gitops-commits"

type commitReport struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
}

// ReportCommit tells the platform once, with the events-callback token of the call ctx serves, that commit landed on
// repository, so the cluster or host reading it pulls at once; the caller logs an error and keeps its commit, since
// the reader's timed pull is the backstop.
func ReportCommit(ctx context.Context, repository, commit string) error {
	base, token, err := PlatformFrom(ctx)
	if err != nil {
		return fmt.Errorf("reporting commit %s on %s: %w", commit, repository, err)
	}
	body, err := json.Marshal(commitReport{Repository: repository, Commit: commit})
	if err != nil {
		return fmt.Errorf("reporting commit %s on %s: %w", commit, repository, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+gitopsCommitsPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("reporting commit %s on %s: %w", commit, repository, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("reporting commit %s on %s: %w", commit, repository, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("reporting commit %s on %s: the platform answered %d", commit, repository, resp.StatusCode)
	}
	return nil
}
