package landing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

// reads is the state as the clause after a check's name in a wait.
func (s checkState) reads() string {
	return [...]string{"has not reported", "failed", "is queued", "is running", "passed"}[s]
}

// checkReport is what one required check last reported on a commit, and the page of the run that reported it when GitHub names one.
type checkReport struct {
	state checkState
	run   string
}

// furthest keeps the report furthest along of two on one check, the earlier of two alike.
func furthest(kept, next checkReport) checkReport {
	if next.state > kept.state {
		return next
	}
	return kept
}

// checkWait is what a removal pull request's required checks hold it on, empty once each has passed; underWay is set only when the held check has reported, queued, running or failed, since a new result for it moves the removal on.
type checkWait struct {
	waitsOn  string
	underWay bool
}

// requiredCheckWait is the one judgement of the checks main requires, by branch protection or by a ruleset, on pr's head, which the unsubscribe and the observer both read.
func (a api) requiredCheckWait(ctx context.Context, pr openPull) (checkWait, error) {
	required, err := a.requiredChecks(ctx)
	if err != nil {
		return checkWait{}, fmt.Errorf("main's required checks cannot be read: %w", err)
	}
	if len(required) == 0 {
		return checkWait{}, nil
	}
	reports, err := a.checkReports(ctx, pr.head, required)
	if err != nil {
		return checkWait{}, fmt.Errorf("its checks cannot be read: %w", err)
	}
	check, held := holdingCheck(reports, required)
	if !held {
		return checkWait{}, nil
	}
	report := reports[check]
	waitsOn := fmt.Sprintf("%s, whose required check %q %s", a.named(pr), check, report.state.reads())
	if report.run != "" {
		waitsOn += fmt.Sprintf(" (run %s)", report.run)
	}
	waitsOn += ". " + waitEndsOnce(fmt.Sprintf("%q passes", check))
	return checkWait{waitsOn: waitsOn, underWay: report.state != checkMissing}, nil
}

// holdRank orders the states that hold a removal back, a higher rank named first in its wait.
type holdRank int

const (
	holdsNothing holdRank = iota
	holdsUnreported
	holdsInFlight
	holdsFailed
)

// holdingCheck names the required check a removal waits on, a failed one before one queued or running, and that before one not reported.
func holdingCheck(reports map[string]checkReport, required []string) (string, bool) {
	holding, rank := "", holdsNothing
	for _, check := range required {
		if r := rankOf(reports[check].state); r > rank {
			holding, rank = check, r
		}
	}
	return holding, rank > holdsNothing
}

func rankOf(s checkState) holdRank {
	switch s {
	case checkFailed:
		return holdsFailed
	case checkQueued, checkRunning:
		return holdsInFlight
	case checkMissing:
		return holdsUnreported
	}
	return holdsNothing
}

// removalUnderWay is what the subscription's open removal pull request waits on while a check main requires is queued, running or failed on it, or while a review main requires is missing, and empty otherwise.
func (a api) removalUnderWay(ctx context.Context, subscription string) (string, error) {
	pr, found, err := a.openPullRequest(ctx, BranchPrefix(subscription)+"remove")
	if err != nil || !found {
		return "", err
	}
	wait, err := a.requiredCheckWait(ctx, pr)
	if err != nil || wait.underWay {
		return wait.waitsOn, err
	}
	return a.reviewWait(ctx, pr)
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

// checkReports reads the check runs on commit, and its commit statuses only when a required check has not passed, tolerating a refused status read while a required check is queued or running.
func (a api) checkReports(ctx context.Context, commit string, required []string) (map[string]checkReport, error) {
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if err := a.repos(ctx, http.MethodGet, "/commits/"+commit+"/check-runs?per_page=100", nil, &runs); err != nil {
		return nil, err
	}
	reports := map[string]checkReport{}
	for _, r := range runs.CheckRuns {
		reports[r.Name] = furthest(reports[r.Name], checkReport{state: runState(r.Status, r.Conclusion), run: r.HTMLURL})
	}
	if allPassed(reports, required) {
		return reports, nil
	}
	var combined struct {
		Statuses []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := a.repos(ctx, http.MethodGet, "/commits/"+commit+"/status", nil, &combined); err != nil {
		if statusOf(err) != http.StatusForbidden {
			return nil, err
		}
		if inFlight(reports, required) {
			return reports, nil
		}
		return nil, a.statusRefused(err, reports, required)
	}
	for _, s := range combined.Statuses {
		reports[s.Context] = furthest(reports[s.Context], checkReport{state: statusState(s.State), run: runPage(s.TargetURL)})
	}
	return reports, nil
}

// runPage is a commit status's target_url without the credentials, query or fragment a CI may carry a token in, and empty unless it is an http or https address.
func runPage(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
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

func allPassed(reports map[string]checkReport, required []string) bool {
	for _, check := range required {
		if reports[check].state != checkPassed {
			return false
		}
	}
	return true
}

func inFlight(reports map[string]checkReport, required []string) bool {
	for _, check := range required {
		if s := reports[check].state; s == checkQueued || s == checkRunning {
			return true
		}
	}
	return false
}

// statusRefused names the permission a refused status read needs, the owner whose installation lacks it and the required checks left undecided.
func (a api) statusRefused(err error, reports map[string]checkReport, required []string) error {
	permission := "statuses=read"
	var s *statusError
	if errors.As(err, &s) && s.accepted != "" {
		permission = s.accepted
	}
	owner, _, _ := strings.Cut(a.repo, "/")
	var open []string
	for _, check := range required {
		if state := reports[check].state; state != checkPassed {
			open = append(open, fmt.Sprintf("%q (%s)", check, state))
		}
	}
	return fmt.Errorf("the installation of %s lacks the permission %s to read commit statuses, so required checks %s cannot be decided: %w", owner, permission, strings.Join(open, ", "), err)
}
