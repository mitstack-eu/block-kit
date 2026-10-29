package landing

import (
	"context"
	"net/http"

	"github.com/mitstack-eu/block-kit/handler"
)

// requiredChecksPassed answers waiting until every check main requires, by branch protection or by a ruleset, has passed on commit.
func (a api) requiredChecksPassed(ctx context.Context, number int, commit string) error {
	required, err := a.requiredChecks(ctx)
	if err != nil {
		return handler.Wait("removal pull request #%d of %s waits, since main's required checks cannot be read: %v", number, a.repo, err)
	}
	if len(required) == 0 {
		return nil
	}
	passed, err := a.passedChecks(ctx, commit, required)
	if err != nil {
		return handler.Wait("removal pull request #%d of %s waits, since its checks cannot be read: %v", number, a.repo, err)
	}
	for _, check := range required {
		if !passed[check] {
			return handler.Wait("removal pull request #%d of %s waits on required check %q, which has not reported success", number, a.repo, check)
		}
	}
	return nil
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

// passedChecks reads the check runs on commit, and its commit statuses only when a required check is still missing.
func (a api) passedChecks(ctx context.Context, commit string, required []string) (map[string]bool, error) {
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := a.repos(ctx, http.MethodGet, "/commits/"+commit+"/check-runs?per_page=100", nil, &runs); err != nil {
		return nil, err
	}
	passed := map[string]bool{}
	for _, r := range runs.CheckRuns {
		passed[r.Name] = passed[r.Name] || r.Conclusion == "success" || r.Conclusion == "neutral" || r.Conclusion == "skipped"
	}
	if allPassed(passed, required) {
		return passed, nil
	}
	var combined struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if err := a.repos(ctx, http.MethodGet, "/commits/"+commit+"/status", nil, &combined); err != nil {
		return nil, err
	}
	for _, s := range combined.Statuses {
		passed[s.Context] = passed[s.Context] || s.State == "success"
	}
	return passed, nil
}

func allPassed(passed map[string]bool, required []string) bool {
	for _, check := range required {
		if !passed[check] {
			return false
		}
	}
	return true
}
