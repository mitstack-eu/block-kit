package landing

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
)

// RequiredApprovals is the most approving reviews branch's full protection or an active ruleset requires, read whole since GitHub's review sub-resource answers 1 while reviews are off.
func RequiredApprovals(ctx context.Context, c *ghapi.Client, repo, branch string) (int, error) {
	return api{c: c, repo: repo}.requiredApprovals(ctx, branch)
}

// StandingReviews counts the approvals on a pull request and says whether a reviewer requested changes, reading each reviewer's latest decision, since GitHub lets a later one or a dismissal take an earlier one back.
func StandingReviews(ctx context.Context, c *ghapi.Client, repo string, number int) (approvals int, changesRequested bool, err error) {
	return api{c: c, repo: repo}.standingReviews(ctx, number)
}

func (a api) requiredApprovals(ctx context.Context, branch string) (int, error) {
	name := url.PathEscape(branch)
	var protection struct {
		Reviews *struct {
			Count int `json:"required_approving_review_count"`
		} `json:"required_pull_request_reviews"`
	}
	required := 0
	err := a.repos(ctx, http.MethodGet, "/branches/"+name+"/protection", nil, &protection)
	switch {
	case statusOf(err) == http.StatusNotFound: // an unprotected branch, which a ruleset can still hold
	case err != nil:
		return 0, err
	case protection.Reviews != nil:
		required = protection.Reviews.Count
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Count int `json:"required_approving_review_count"`
		} `json:"parameters"`
	}
	if err := a.repos(ctx, http.MethodGet, "/rules/branches/"+name, nil, &rules); err != nil {
		return 0, err
	}
	for _, rule := range rules {
		if rule.Type == "pull_request" {
			required = max(required, rule.Parameters.Count)
		}
	}
	return required, nil
}

func (a api) standingReviews(ctx context.Context, number int) (approvals int, changesRequested bool, err error) {
	var reviews []struct {
		State string `json:"state"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := a.repos(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d/reviews?per_page=100", number), nil, &reviews); err != nil {
		return 0, false, err
	}
	latest := map[string]string{}
	for _, review := range reviews {
		if review.State != "COMMENTED" && review.State != "PENDING" {
			latest[review.User.Login] = review.State
		}
	}
	for _, state := range latest {
		switch state {
		case "APPROVED":
			approvals++
		case "CHANGES_REQUESTED":
			changesRequested = true
		}
	}
	return approvals, changesRequested, nil
}

// reviewWait is what an open removal pull request waits on while main requires more approving reviews than it holds or a reviewer requested changes, and empty otherwise.
func (a api) reviewWait(ctx context.Context, pr openPull) (string, error) {
	required, err := a.requiredApprovals(ctx, "main")
	if err != nil || required == 0 {
		return "", err
	}
	approvals, changesRequested, err := a.standingReviews(ctx, pr.number)
	if err != nil || approvals >= required && !changesRequested {
		return "", err
	}
	waitsOn := a.named(pr) + ", which waits on an approving review"
	ending := "an approving review is given"
	if changesRequested {
		waitsOn += ", and a reviewer requested changes"
		ending = "an approving review leaves no reviewer requesting changes"
	}
	return waitsOn + ". " + waitEndsOnce(ending), nil
}
