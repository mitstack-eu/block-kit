package ghapi_test

// A GitHub answer a retry cannot change is a classed refusal carrying GitHub's message, and a rate limit or a server error stays retryable.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/handler"
)

const (
	planMessage   = "Upgrade to GitHub Pro or make this repository public to enable this feature."
	planAnswer    = `{"message":"` + planMessage + `","documentation_url":"https://docs.github.com/rest/repos/rules#create-a-repository-ruleset","status":"403"}`
	nameTaken     = "name already exists on this account"
	nameAnswer    = `{"message":"Repository creation failed.","errors":[{"resource":"Repository","code":"custom","field":"name","message":"` + nameTaken + `"}]}`
	createRepos   = "/orgs/acme/repos"
	rulesetRoute  = "POST /repos/acme/widget/rulesets"
	secondaryRate = "You have exceeded a secondary rate limit. Please wait a few minutes before you try again."
)

func TestAnswerError(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		header  http.Header
		body    string
		refused bool
		has     []string
	}{
		{name: "a 403 naming the plan GitHub needs is refused with GitHub's message", status: http.StatusForbidden, body: planAnswer,
			refused: true, has: []string{rulesetRoute + " returned 403", planMessage}},
		{name: "a 422 carrying field errors is refused with each of GitHub's messages", status: http.StatusUnprocessableEntity, body: nameAnswer,
			refused: true, has: []string{"Repository creation failed.", nameTaken}},
		{name: "a 403 whose body is no JSON is refused with its text", status: http.StatusForbidden, body: "forbidden by policy",
			refused: true, has: []string{"forbidden by policy"}},
		{name: "a 403 whose message echoes the token is refused without carrying it", status: http.StatusForbidden, body: `{"message":"token ` + token + ` lacks administration"}`,
			refused: true, has: []string{"lacks administration"}},
		{name: "a 429 stays retryable", status: http.StatusTooManyRequests, body: `{"message":"API rate limit exceeded"}`, has: []string{"returned 429"}},
		{name: "a 403 with no requests left in the rate limit stays retryable", status: http.StatusForbidden,
			header: http.Header{"X-Ratelimit-Remaining": {"0"}}, body: `{"message":"API rate limit exceeded for installation."}`, has: []string{"returned 403"}},
		{name: "a 403 asking to retry after a while stays retryable", status: http.StatusForbidden,
			header: http.Header{"Retry-After": {"60"}}, body: `{"message":"forbidden"}`, has: []string{"returned 403"}},
		{name: "a 403 naming a secondary rate limit stays retryable", status: http.StatusForbidden, body: `{"message":"` + secondaryRate + `"}`, has: []string{secondaryRate}},
		{name: "a 502 stays retryable", status: http.StatusBadGateway, body: "<html>bad gateway</html>", has: []string{"returned 502"}},
		{name: "a 404 stays a plain error", status: http.StatusNotFound, body: `{"message":"Not Found"}`, has: []string{"returned 404"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ghapi.AnswerError(rulesetRoute, tc.status, tc.header, []byte(tc.body), token)

			if err == nil {
				t.Fatal("AnswerError returned nil, want an error")
			}
			var refusal *handler.Refused
			if isRefusal := errors.As(err, &refusal); isRefusal != tc.refused || (isRefusal && refusal.Class != ghapi.ClassGitHubRefused) {
				t.Errorf("error = %#v, want refused %v with class %q", err, tc.refused, ghapi.ClassGitHubRefused)
			}
			if tc.refused && !strings.HasPrefix(err.Error(), "refused:"+ghapi.ClassGitHubRefused+": ") {
				t.Errorf("error = %q, want the statusDetail shape refused:%s: <message>", err, ghapi.ClassGitHubRefused)
			}
			for _, want := range tc.has {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to say %q", err, want)
				}
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("error = %q carries the token", err)
			}
		})
	}
}

func TestEnsureRepo_RefusesWhatARetryCannotChangeAndRetriesTheRest(t *testing.T) {
	ensure := ensureWith(appID)
	newRepo := map[string][]reply{get(widget): {notFound}, post(createRepos): {{http.StatusCreated, `{"full_name":"acme/widget"}`}}, get(listed): {listingNothing}}
	with := func(base map[string][]reply, key string, r reply) map[string][]reply {
		out := map[string][]reply{key: {r}}
		for k, v := range base {
			if k != key {
				out[k] = v
			}
		}
		return out
	}
	existing := map[string][]reply{get(widget): {{}}, get(listed): {listingNothing}}
	runAPICases(t, []apiCase{
		{name: "a ruleset create refused for the plan fails at once with GitHub's message",
			routes: with(newRepo, post(rulesets), reply{http.StatusForbidden, planAnswer}),
			call:   ensure, want: "", err: errAny, refused: true, errHas: planMessage,
			requests: []string{get(widget), post(createRepos), get(listed), post(rulesets)}},
		{name: "a rulesets listing refused for the plan fails at once with GitHub's message",
			routes: with(existing, get(listed), reply{http.StatusForbidden, planAnswer}),
			call:   ensure, want: "", err: errAny, refused: true, errHas: planMessage, requests: []string{get(widget), get(listed)}},
		{name: "a ruleset update refused on an upgrade fails at once with GitHub's message",
			routes: map[string][]reply{get(widget): {{}}, get(listed): {listingOurs}, get(ours): {held(func(r map[string]any) { r["enforcement"] = "disabled" })},
				"PUT " + ours: {{http.StatusForbidden, planAnswer}}},
			call: ensure, want: "", err: errAny, refused: true, errHas: planMessage, requests: []string{get(widget), get(listed), get(ours), "PUT " + ours}},
		{name: "a repository create refused as invalid fails at once with GitHub's message",
			routes: map[string][]reply{get(widget): {notFound}, post(createRepos): {{http.StatusUnprocessableEntity, nameAnswer}}},
			call:   ensure, want: "", err: errAny, refused: true, errHas: nameTaken, requests: []string{get(widget), post(createRepos)}},
		{name: "a repository create answered 429 stays retryable",
			routes: map[string][]reply{get(widget): {notFound}, post(createRepos): {{http.StatusTooManyRequests, `{"message":"API rate limit exceeded"}`}}},
			call:   ensure, want: "", err: errAny, retryable: true, errHas: "returned 429", requests: []string{get(widget), post(createRepos)}},
		{name: "a ruleset create answered 403 out of rate limit stays retryable",
			routes:  with(newRepo, post(rulesets), reply{http.StatusForbidden, `{"message":"API rate limit exceeded for installation."}`}),
			headers: map[string]http.Header{post(rulesets): {"X-Ratelimit-Remaining": {"0"}}},
			call:    ensure, want: "", err: errAny, retryable: true, errHas: "returned 403",
			requests: []string{get(widget), post(createRepos), get(listed), post(rulesets)}},
		{name: "a ruleset create answered 502 after the repository exists is retried, never refused",
			routes: with(newRepo, post(rulesets), reply{http.StatusBadGateway, "bad gateway"}),
			call:   ensure, want: "", err: errAny, retryable: true, errHas: "returned 502",
			requests: []string{get(widget), post(createRepos), get(listed), post(rulesets)}},
		{name: "a re-run once the cause is fixed reuses the repository and adds the ruleset",
			routes: with(existing, post(rulesets), reply{http.StatusCreated, `{"id":42}`}),
			call:   ensure, want: "acme/widget", requests: []string{get(widget), get(listed), post(rulesets)}, check: sent(post(rulesets), appID)},
		{name: "a repository delete answered 503 stays retryable",
			routes: map[string][]reply{"DELETE " + widget: {{http.StatusServiceUnavailable, "unavailable"}}},
			call:   func(c *ghapi.Client) (any, error) { return nil, c.DeleteRepo(context.Background(), "acme", "widget") },
			err:    errAny, retryable: true, errHas: "returned 503", requests: []string{"DELETE " + widget}},
	})
}
