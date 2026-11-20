package cloud

import (
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mitstack-eu/block-kit/handler"
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

// Holds keeps each held token by a SHA-256 of the token in the operator's memory, so a restarted operator learns of the limit again from its first refused call.
type Holds struct {
	now  func() time.Time
	mu   sync.Mutex
	held map[[sha256.Size]byte]*hold
}

// hold's streak counts the refusals in a row before the one that set it, and probing is set while the one call sent once the hold ended has no answer yet.
type hold struct {
	until   time.Time
	streak  int
	probing bool
}

func NewHolds(now func() time.Time) *Holds {
	return &Holds{now: now, held: map[[sha256.Size]byte]*hold{}}
}

// processHolds is shared by every client this package returns, so all of an operator's calls on one token meet one hold.
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
	key := sha256.Sum256([]byte(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")))
	probe, err := t.holds.admit(key)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	if probe != nil {
		defer t.holds.inconclusive(key, probe)
	}
	resp, err := t.next.RoundTrip(req)
	switch {
	case err != nil || resp.StatusCode >= http.StatusInternalServerError:
		return resp, err
	case resp.StatusCode != http.StatusTooManyRequests:
		t.holds.answered(key, probe)
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswerBytes))
	_ = resp.Body.Close()
	return nil, t.holds.refused(key, probe, resp.Header)
}

func (h *Holds) admit(key [sha256.Size]byte) (probe *hold, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.held[key]
	switch {
	case cur == nil:
		return nil, nil
	case cur.probing || h.now().Before(cur.until):
		return nil, h.limited(cur.until)
	}
	cur.probing = true
	return cur, nil
}

func (h *Holds) answered(key [sha256.Size]byte, probe *hold) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if probe != nil && h.held[key] == probe {
		delete(h.held, key)
	}
}

// inconclusive keeps the hold for the next call to test, since neither a lost call, a server failure nor a panic says the limit has passed, and does nothing once answered has removed or refused has replaced the probe's hold.
func (h *Holds) inconclusive(key [sha256.Size]byte, probe *hold) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if probe != nil && h.held[key] == probe {
		probe.probing = false
	}
}

// refused holds key after a refusal, keeping a hold another call's refusal already started, since a call sent before it could otherwise shorten it.
func (h *Holds) refused(key [sha256.Size]byte, probe *hold, header http.Header) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur := h.held[key]; cur != nil && cur != probe {
		return h.limited(cur.until)
	}
	streak := 0
	if probe != nil {
		streak = probe.streak + 1
	}
	now := h.now()
	until := now.Add(grow(firstHold, longestHold, streak))
	if reset, ok := resetTime(header); ok && reset.After(now) && reset.Before(until) {
		until = reset
	}
	h.held[key] = &hold{until: until, streak: streak}
	return &RateLimited{Until: until}
}

func (h *Holds) limited(until time.Time) *RateLimited {
	if !h.now().Before(until) {
		return &RateLimited{}
	}
	return &RateLimited{Until: until}
}

const (
	firstPoll   = time.Second
	longestPoll = 15 * time.Second
)

// PollBackoff is the BackoffFunc a block hands hcloud-go's action waiter through hcloud.WithPollOpts: a short first wait sees a quick action soon, and the waits grow to longestPoll since every ask spends a call of the budget all operators on one address share.
func PollBackoff(retries int) time.Duration { return grow(firstPoll, longestPoll, retries) }

// grow is the package's one growth rule, shared by the hold and PollBackoff: first doubled n times, stopping at ceiling so no count overflows.
func grow(first, ceiling time.Duration, n int) time.Duration {
	d := first
	for i := 0; i < n && d < ceiling; i++ {
		d *= 2
	}
	return min(d, ceiling)
}

func resetTime(header http.Header) (time.Time, bool) {
	secs, err := strconv.ParseInt(header.Get("RateLimit-Reset"), 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}
