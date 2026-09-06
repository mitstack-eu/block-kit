// Package delivery is the operator-side mirror of the dispatcher's delivery
// record: a typed Record joining a registered build spec with its gate
// verdict, build result and deploy outcome, plus a Client that fetches one
// over GET /delivery/{appId}/{sha}.
//
// testdata/delivery.golden.json is shared with the dispatcher's own
// index.test.mjs: the same bytes are both the JSON the dispatcher's handler
// must produce and the JSON this package must round-trip through Record
// without loss. Editing either side alone breaks a test, which is the point
// of a shared golden file — the writer and the reader of one record stay
// proven together.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Conclusion values a build result carries.
const (
	ConclusionSuccess = "success"
	ConclusionFailure = "failure"
)

// Phase values a delivery record carries, in the precedence order the
// dispatcher derives them: a reported deploy-state transition beats a failed
// build, which beats a failed gate verdict, which beats a passed one, which
// beats a successful build with nothing else reported yet.
const (
	PhaseBuilding    = "building"
	PhaseBuildFailed = "build-failed"
	PhaseGatesFailed = "gates-failed"
	PhaseDeploying   = "deploying"
	PhaseHealthy     = "healthy"
	PhaseRolledBack  = "rolled-back"
)

// BuildResult is how a build itself concluded, distinct from the deploy that
// may have followed it.
type BuildResult struct {
	Conclusion string `json:"conclusion"`
	RunURL     string `json:"runUrl"`
}

// Record is the joined delivery record the dispatcher serves. Field order
// matches the golden fixture's key order; Verdict keeps the verdict's own
// raw JSON bytes rather than re-encoding through a Go struct, so a round
// trip through Record reproduces the golden byte for byte.
type Record struct {
	AppID       string          `json:"appId"`
	SHA         string          `json:"sha"`
	Repo        string          `json:"repo"`
	Verdict     json.RawMessage `json:"verdict"`
	DeployState *string         `json:"deployState"`
	BuildResult *BuildResult    `json:"buildResult"`
	Phase       string          `json:"phase"`
}

// ErrNotFound is the error a 404 response wraps: no build is registered for
// the requested appId.
var ErrNotFound = errors.New("delivery: no registered build found")

// maxErrorBodyBytes caps how much of a non-200, non-404 response body a
// StatusError carries, so a runaway or malicious response body never grows
// the error without bound.
const maxErrorBodyBytes = 4096

// StatusError is returned for any response status Client.Get does not
// otherwise interpret (neither 200 nor 404).
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("delivery: unexpected status %d: %s", e.StatusCode, e.Body)
}

// Client fetches a delivery Record from the dispatcher.
type Client struct {
	HTTP  *http.Client
	Base  string
	Token string
}

// NewClient builds a Client against baseURL, authenticating with token. A nil
// httpClient falls back to http.DefaultClient.
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{HTTP: httpClient, Base: baseURL, Token: token}
}

// httpClient resolves the client to send with at request time, so a Client
// built by struct literal with a nil HTTP still works.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Get fetches the delivery record for appID at sha. A 404 response returns
// an error satisfying errors.Is(err, ErrNotFound); any other non-200 status
// returns a *StatusError carrying the response body. Neither error path ever
// includes the bearer token.
func (c *Client) Get(ctx context.Context, appID, sha string) (*Record, error) {
	url := strings.TrimRight(c.Base, "/") + "/delivery/" + appID + "/" + sha

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("delivery: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("delivery: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w for %s/%s", ErrNotFound, appID, sha)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, &StatusError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var rec Record
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return nil, fmt.Errorf("delivery: decode response: %w", err)
	}
	return &rec, nil
}
