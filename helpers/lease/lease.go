// Package lease, an optional helper tied to no provider, asks the platform's lease route for one short-lived credential of a resource the asking node's block requires or provides.
package lease

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/bounded"
)

const (
	routePath = "/v1/cluster-leases"
	// maxAnswerBytes fits the largest credential the route passes on, 48 KiB, with its expiry.
	maxAnswerBytes = 64 << 10
	askTimeout     = 10 * time.Second
	redacted       = "[REDACTED]"
)

// Platform is where a node asks for a lease and the bearer the route knows it by, the report token the node received at enrolment.
type Platform struct {
	BaseURL string
	Token   string
	// HTTPClient defaults to one that waits ten seconds at most and follows no redirect.
	HTTPClient *http.Client
	// Clock judges an answer's expiry and defaults to time.Now.
	Clock func() time.Time
}

// Answer is a lease the route granted: the provider's credential, passed on unread, and when it expires.
type Answer struct {
	Credential json.RawMessage
	ExpiresAt  time.Time
}

// Refusal is an answer of the route that grants no lease, and RetryAfter is the wait a 503 names, zero when the answer names none.
type Refusal struct {
	Status     int
	Resource   string
	Message    string
	RetryAfter time.Duration
}

func (r *Refusal) Error() string {
	text := fmt.Sprintf("lease of %q: the platform answered %d: %s", r.Resource, r.Status, r.Message)
	if r.RetryAfter > 0 {
		text += fmt.Sprintf(" (Retry-After %s)", r.RetryAfter)
	}
	return text
}

type askBody struct {
	Resource string `json:"resource"`
	Want     any    `json:"want,omitempty"`
}

type answerBody struct {
	Credential json.RawMessage `json:"credential"`
	ExpiresAt  *time.Time      `json:"expiresAt"`
	Error      string          `json:"error"`
}

// Ask asks the route once for a credential of resource shaped by want, a value that marshals to a JSON object or nil, and leaves it to the caller when to ask again after a *Refusal.
func (p Platform) Ask(ctx context.Context, resource string, want any) (Answer, error) {
	req, err := p.request(ctx, resource, want)
	if err != nil {
		return Answer{}, err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return Answer{}, fmt.Errorf("lease of %q: %w", resource, err)
	}
	defer resp.Body.Close()
	raw, err := bounded.Read(resp, "lease of "+strconv.Quote(resource), maxAnswerBytes)
	if err != nil {
		return Answer{}, err
	}
	var answer answerBody
	decodeErr := json.Unmarshal(raw, &answer)
	if resp.StatusCode != http.StatusOK {
		return Answer{}, p.refusal(resp, resource, answer.Error)
	}
	if decodeErr != nil {
		return Answer{}, fmt.Errorf("lease of %q: the platform's answer is not JSON", resource)
	}
	return p.usable(resource, answer)
}

func (p Platform) request(ctx context.Context, resource string, want any) (*http.Request, error) {
	if p.BaseURL == "" || p.Token == "" {
		return nil, fmt.Errorf("lease of %q: the platform's address and the node's bearer are both needed", resource)
	}
	body, err := json.Marshal(askBody{Resource: resource, Want: want})
	if err != nil {
		return nil, fmt.Errorf("lease of %q: encoding the request: %w", resource, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(p.BaseURL, "/")+routePath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("lease of %q: the platform's address is not a URL", resource)
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (p Platform) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return defaultClient
}

// defaultClient follows no redirect, since a redirect would carry the ask to a host the caller did not name.
var defaultClient = &http.Client{Timeout: askTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (p Platform) now() time.Time {
	if p.Clock != nil {
		return p.Clock()
	}
	return time.Now()
}

// refusal redacts the bearer from the route's message, so an answer that repeats it cannot carry it into a log.
func (p Platform) refusal(resp *http.Response, resource, message string) *Refusal {
	if message == "" {
		message = "no reason given"
	}
	return &Refusal{
		Status:     resp.StatusCode,
		Resource:   resource,
		Message:    strings.ReplaceAll(message, p.Token, redacted),
		RetryAfter: retryAfter(resp),
	}
}

// retryAfter reads the seconds form of Retry-After, the form the route sends.
func retryAfter(resp *http.Response) time.Duration {
	seconds, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (p Platform) usable(resource string, answer answerBody) (Answer, error) {
	if len(answer.Credential) == 0 || string(answer.Credential) == "null" {
		return Answer{}, fmt.Errorf("lease of %q: the platform's answer holds no credential", resource)
	}
	if answer.ExpiresAt == nil {
		return Answer{}, fmt.Errorf("lease of %q: the platform's answer holds no expiresAt", resource)
	}
	if !answer.ExpiresAt.After(p.now()) {
		return Answer{}, fmt.Errorf("lease of %q: the credential expired at %s, before it arrived", resource, answer.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return Answer{Credential: answer.Credential, ExpiresAt: *answer.ExpiresAt}, nil
}
