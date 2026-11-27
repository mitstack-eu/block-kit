package cloud

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/callhold"
	"github.com/mitstack-eu/block-kit/helpers/calllog"
)

// ErrRateLimited matches the error of a call Hetzner refused for its rate limit, and of every call held unsent on that token after it.
var ErrRateLimited = errors.New("Hetzner Cloud's rate limit is reached")

// A refusal holds its token for firstHold, doubled by each refusal in a row up to longestHold, since Hetzner refills its budget one call a second while its reset names when the whole budget is back.
const (
	firstHold   = time.Minute
	longestHold = time.Hour
)

// RateLimited is a refused or held call's error, which wraps a handler.Waiting so the kit's handler answers the step waiting, and has no Timeout method, since hcloud-go's retry policy sends a call again when a net.Error reports a timeout.
type RateLimited struct {
	// Until is when the hold ends, and zero once it has ended while no call has yet shown the limit passed, as for a call meeting the probe in flight or a refusal answered late.
	Until time.Time
}

func (e *RateLimited) Error() string {
	if e.Until.IsZero() {
		return "Hetzner Cloud refused this token's calls for its rate limit, and no call has shown the limit passed since its hold ended"
	}
	return "Hetzner Cloud refused this token's calls for its rate limit, so none is sent until " + e.Until.UTC().Format(time.RFC3339)
}

func (e *RateLimited) Is(target error) bool { return target == ErrRateLimited }

func (e *RateLimited) Unwrap() error {
	return &handler.Waiting{Detail: e.Error(), LookAgainAt: e.Until}
}

type Holds struct{ held *callhold.Holds }

func NewHolds(now func() time.Time) *Holds {
	return &Holds{held: callhold.New(now, firstHold, longestHold)}
}

var processHolds = NewHolds(time.Now)

// HTTPClient is the client a block's hcloud-go client takes through hcloud.WithHTTPClient, holding the same tokens as every Client.
func HTTPClient() *http.Client { return processHolds.HTTPClient() }

// HTTPClient answers a call on a held token with a RateLimited error, unsent, and wraps the call log, so a refusal's line reads its 429 and a held call writes no line.
func (h *Holds) HTTPClient() *http.Client {
	return &http.Client{Timeout: timeout, Transport: &holdTransport{holds: h, next: calllog.Transport(nil)}}
}

func (h *Holds) Client(base, token string) *Client {
	return &Client{base: base, token: token, http: h.HTTPClient()}
}

type holdTransport struct {
	holds *Holds
	next  http.RoundTripper
}

func (t *holdTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	call, until, ok := t.holds.held.Admit(callhold.KeyOf(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")))
	if !ok {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, &RateLimited{Until: until}
	}
	defer call.Inconclusive()
	resp, err := t.next.RoundTrip(req)
	switch {
	case err != nil || resp.StatusCode >= http.StatusInternalServerError:
		return resp, err
	case resp.StatusCode != http.StatusTooManyRequests:
		call.Answered()
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswerBytes))
	_ = resp.Body.Close()
	return nil, &RateLimited{Until: call.Refused(func(now, until time.Time) time.Time {
		if reset, ok := resetTime(resp.Header); ok && reset.After(now) && reset.Before(until) {
			return reset
		}
		return until
	})}
}

const (
	firstPoll   = time.Second
	longestPoll = 15 * time.Second
)

// PollBackoff is the BackoffFunc a block hands hcloud-go's action waiter through hcloud.WithPollOpts: a short first wait sees a quick action soon, and the waits grow to longestPoll since every ask spends a call of the budget all operators on one address share.
func PollBackoff(retries int) time.Duration { return callhold.Grow(firstPoll, longestPoll, retries) }

func resetTime(header http.Header) (time.Time, bool) {
	secs, err := strconv.ParseInt(header.Get("RateLimit-Reset"), 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}
