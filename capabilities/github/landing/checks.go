package landing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mitstack-eu/block-kit/handler"
)

// checkState is what one required check last reported on a commit, ordered so the furthest along of two reports wins; the zero value has not reported.
type checkState int

const (
	checkMissing checkState = iota
	checkFailed
	checkQueued
	checkRunning
	checkPassed
)

func (s checkState) String() string {
	return [...]string{"not reported", "failed", "queued", "running", "passed"}[s]
}

// requiredChecksPassed answers waiting on the removal until every check main requires, by branch protection or by a ruleset, has passed on commit.
func (a api) requiredChecksPassed(ctx context.Context, number int, commit string) error {
	required, err := a.requiredChecks(ctx)
	if err != nil {
		return handler.Wait("removal pull request #%d of %s waits, since main's required checks cannot be read: %v", number, a.repo, err)
	}
	if len(required) == 0 {
		return nil
	}
	states, err := a.checkStates(ctx, commit, required)
	if err != nil {
		return handler.Wait("removal pull request #%d of %s waits, since its checks cannot be read: %v", number, a.repo, err)
	}
	for _, check := range required {
		if states[check] != checkPassed {
			return handler.WaitOnRemoval("removal pull request #%d of %s waits on required check %q, which has not reported success", number, a.repo, check)
		}
	}
	return nil
}

// removalUnderWay is what the subscription's open removal pull request waits on while a check main requires is queued or running on it and none has failed, and empty otherwise.
func (a api) removalUnderWay(ctx context.Context, subscription string) (string, error) {
	pr, found, err := a.openPullRequest(ctx, BranchPrefix(subscription)+"remove")
	if err != nil || !found {
		return "", err
	}
	required, err := a.requiredChecks(ctx)
	if err != nil || len(required) == 0 {
		return "", err
	}
	states, err := a.checkStates(ctx, pr.head, required)
	if err != nil {
		return "", err
	}
	waitsOn := ""
	for _, check := range required {
		switch states[check] {
		case checkFailed:
			return "", nil
		case checkQueued, checkRunning:
			if waitsOn == "" {
				waitsOn = fmt.Sprintf("removal pull request #%d of %s, whose required check %q is %s", pr.number, a.repo, check, states[check])
			}
		}
	}
	return waitsOn, nil
}

func (a api) requiredChecks(ctx context.Context) ([]string, error) {
	var branch struct {
		Protection struct {
			RequiredStatusChecks struct {
				Contexts []string `json:"contexts"`
				Checks   []struct {
					Context string `json:"context"`
				} `json:"checks"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	if err := a.repos(ctx, http.MethodGet, "/branches/main", nil, &branch); err != nil {
		return nil, err
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredStatusChecks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := a.repos(ctx, http.MethodGet, "/rules/branches/main", nil, &rules); err != nil {
		return nil, err
	}
	required := branch.Protection.RequiredStatusChecks.Contexts
	for _, c := range branch.Protection.RequiredStatusChecks.Checks {
		required = append(required, c.Context)
	}
	for _, r := range rules {
		if r.Type != "required_status_checks" {
			continue
		}
		for _, c := range r.Parameters.RequiredStatusChecks {
			required = append(required, c.Context)
		}
	}
	return required, nil
}

// checkStates reads the check runs on commit, and its commit statuses only when a required check has not passed. A refused status read answers
// with the check runs alone while one required check is queued or running, since the wait then holds whatever the statuses say.
func (a api) checkStates(ctx context.Context, commit string, required []string) (map[string]checkState, error) {
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := a.repos(ctx, http.MethodGet, "/commits/"+commit+"/check-runs?per_page=100", nil, &runs); err != nil {
		return nil, err
	}
	states := map[string]checkState{}
	for _, r := range runs.CheckRuns {
		states[r.Name] = max(states[r.Name], runState(r.Status, r.Conclusion))
	}
	if allPassed(states, required) {
		return states, nil
	}
	var combined struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if err := a.repos(ctx, http.MethodGet, "/commits/"+commit+"/status", nil, &combined); err != nil {
		if statusOf(err) != http.StatusForbidden {
			return nil, err
		}
		if inFlight(states, required) {
			return states, nil
		}
		return nil, a.statusRefused(err, states, required)
	}
	for _, s := range combined.Statuses {
		states[s.Context] = max(states[s.Context], statusState(s.State))
	}
	return states, nil
}

func runState(status, conclusion string) checkState {
	switch {
	case status == "in_progress":
		return checkRunning
	case status != "completed":
		return checkQueued
	case conclusion == "success" || conclusion == "neutral" || conclusion == "skipped":
		return checkPassed
	}
	return checkFailed
}

func statusState(state string) checkState {
	switch state {
	case "success":
		return checkPassed
	case "pending":
		return checkQueued
	}
	return checkFailed
}

func allPassed(states map[string]checkState, required []string) bool {
	for _, check := range required {
		if states[check] != checkPassed {
			return false
		}
	}
	return true
}

func inFlight(states map[string]checkState, required []string) bool {
	for _, check := range required {
		if states[check] == checkQueued || states[check] == checkRunning {
			return true
		}
	}
	return false
}

// statusRefused names the permission the refused read needs, the owner whose installation lacks it and the required checks it leaves undecided.
func (a api) statusRefused(err error, states map[string]checkState, required []string) error {
	permission := "statuses=read"
	var s *statusError
	if errors.As(err, &s) && s.accepted != "" {
		permission = s.accepted
	}
	owner, _, _ := strings.Cut(a.repo, "/")
	var open []string
	for _, check := range required {
		if states[check] != checkPassed {
			open = append(open, fmt.Sprintf("%q (%s)", check, states[check]))
		}
	}
	return fmt.Errorf("the installation of %s lacks the permission %s to read commit statuses, so required checks %s cannot be decided: %w", owner, permission, strings.Join(open, ", "), err)
}
