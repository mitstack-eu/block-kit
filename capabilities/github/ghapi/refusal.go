package ghapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/safe"
)

// ClassGitHubRefused is the class of a GitHub answer a retry cannot change, such as a ruleset a private repository's plan cannot hold.
const ClassGitHubRefused = "github-refused"

// AnswerError is the error for GitHub's answer of status to route: a refusal carrying GitHub's message for a 403 or 422 that is no rate limit, and a plain, retryable error for any other.
func AnswerError(route string, status int, header http.Header, body []byte, token string) error {
	text := safe.LogSafe(answerMessage(body), token)
	if refusesForGood(status, header, text) {
		return handler.Refuse(ClassGitHubRefused, "github %s returned %d: %s", route, status, text)
	}
	return fmt.Errorf("ghapi: github %s returned %d: %s", route, status, text)
}

// refusesForGood reads a 403 as a rate limit when GitHub sends a reset signal or names one, since GitHub answers a spent rate limit 403 as often as 429.
func refusesForGood(status int, header http.Header, text string) bool {
	switch status {
	case http.StatusUnprocessableEntity:
		return true
	case http.StatusForbidden:
		return header.Get("Retry-After") == "" && header.Get("X-RateLimit-Remaining") != "0" &&
			!strings.Contains(strings.ToLower(text), "rate limit")
	}
	return false
}

// answerMessage is GitHub's message followed by each of its errors, which GitHub sends as strings or as field objects, or the body as sent when it holds no message.
func answerMessage(body []byte) string {
	var answer struct {
		Message string            `json:"message"`
		Errors  []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(body, &answer) != nil || answer.Message == "" {
		return strings.TrimSpace(string(body))
	}
	parts := []string{answer.Message}
	for _, raw := range answer.Errors {
		var text string
		var field struct{ Message, Code, Field string }
		switch {
		case json.Unmarshal(raw, &text) == nil && text != "":
			parts = append(parts, text)
		case json.Unmarshal(raw, &field) != nil:
		case field.Message != "":
			parts = append(parts, field.Message)
		case field.Code != "":
			parts = append(parts, strings.TrimSpace(field.Field+" "+field.Code))
		}
	}
	return strings.Join(parts, " ")
}
