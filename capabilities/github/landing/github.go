package landing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/helpers/safe"
)

// maxBody bounds a read, since a pull request or ref listing is small and no answer here carries file content.
const maxBody = 1 << 20

// api calls the GitHub routes ghapi has no method for, on one repository.
type api struct {
	c    *ghapi.Client
	repo string
}

// statusError is an answer outside 2xx, its body scrubbed of the token.
type statusError struct {
	call   string
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("landing: github %s answered %d: %s", e.call, e.status, e.body)
}

func statusOf(err error) int {
	var s *statusError
	if errors.As(err, &s) {
		return s.status
	}
	return 0
}

// repos calls path under the repository.
func (a api) repos(ctx context.Context, method, path string, payload, out any) error {
	return a.call(ctx, method, "/repos/"+a.repo+path, payload, out)
}

// call decodes a 2xx answer into out and returns any other as a *statusError.
func (a api) call(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("landing: encode %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.c.Base+path, body)
	if err != nil {
		return fmt.Errorf("landing: build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+a.c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	httpClient := a.c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("landing: github %s %s unreachable: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("landing: read the answer to %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return &statusError{call: method + " " + path, status: resp.StatusCode, body: safe.LogSafe(string(raw), a.c.Token)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("landing: decode the answer to %s %s: %w", method, path, err)
	}
	return nil
}
