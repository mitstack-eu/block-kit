// Package lease, an optional helper tied to no provider, asks the platform's lease route for one short-lived credential of a resource the asking node's block requires or provides.
package lease

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	// maxWaitSeconds is the longest Retry-After a time.Duration can hold.
	maxWaitSeconds    = math.MaxInt64 / int64(time.Second)
	unreadableMessage = "the answer is not the route's JSON"
	withheldMessage   = "the route's message is withheld, since it holds an address"
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

// Refusal is an answer of the route that grants no lease, and RetryAfter is the wait a 503 names, zero when the answer names none it can hold.
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
	raw, readErr := bounded.Read(resp, "lease of "+strconv.Quote(resource), maxAnswerBytes)
	if resp.StatusCode != http.StatusOK {
		return Answer{}, &Refusal{Status: resp.StatusCode, Resource: resource, Message: p.message(raw), RetryAfter: p.retryAfter(resp)}
	}
	if readErr != nil {
		return Answer{}, readErr
	}
	var answer answerBody
	if err := json.Unmarshal(raw, &answer); err != nil {
		return Answer{}, fmt.Errorf("lease of %q: the platform's answer is not the JSON of a lease", resource)
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

// message passes on the route's own message only when it holds no address, with the bearer redacted, so a refusal cannot carry a secret into a log.
func (p Platform) message(raw []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Error == "" {
		return unreadableMessage
	}
	if strings.Contains(body.Error, "://") {
		return withheldMessage
	}
	return strings.ReplaceAll(body.Error, p.Token, redacted)
}

// retryAfter reads both forms of Retry-After, seconds and a date, as the wait from now.
func (p Platform) retryAfter(resp *http.Response) time.Duration {
	header := resp.Header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil {
		if seconds < 0 || seconds > maxWaitSeconds {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil && at.After(p.now()) {
		return at.Sub(p.now())
	}
	return 0
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
