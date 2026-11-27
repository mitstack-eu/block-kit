package s3store

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mitstack-eu/block-kit/handler"
)

// ErrSlowDown matches the error of a call the store answered 503 SlowDown, and of every call held unsent on that key after it.
var ErrSlowDown = errors.New("the S3-compatible store asked this key's calls to slow down")

// A SlowDown pauses its key for firstPause, doubled by each SlowDown in a row up to longestPause, or until the store's Retry-After, up to longestPause, when the SlowDown names one.
const (
	firstPause    = 30 * time.Second
	longestPause  = 15 * time.Minute
	maxErrorBytes = 64 << 10
)

// SlowDown is a refused or held call's error, which wraps a handler.Waiting so the kit's handler answers the step waiting until the pause ends.
type SlowDown struct {
	// Until is when the pause ends, and zero once it has ended while no call has yet shown the store answering again.
	Until time.Time
}

func (e *SlowDown) Error() string {
	if e.Until.IsZero() {
		return "the store asked this key's calls to slow down, and no call has shown it answering again since the pause ended"
	}
	return "the store asked this key's calls to slow down, so none is sent until " + e.Until.UTC().Format(time.RFC3339)
}

func (e *SlowDown) Is(target error) bool { return target == ErrSlowDown }

func (e *SlowDown) Unwrap() error {
	return &handler.Waiting{Detail: e.Error(), LookAgainAt: e.Until}
}

// holds keeps each paused key by a SHA-256 of the access key in the operator's memory, so a restarted operator learns of the pause again from its first refused call.
type holds struct {
	now    func() time.Time
	mu     sync.Mutex
	paused map[[sha256.Size]byte]*pause
}

// pause's streak counts the SlowDown answers in a row before the one that set it, and probing is set while the one call sent once the pause ended has no answer yet.
type pause struct {
	until   time.Time
	streak  int
	probing bool
}

func newHolds(now func() time.Time) *holds {
	return &holds{now: now, paused: map[[sha256.Size]byte]*pause{}}
}

// processHolds is shared by every client New returns, so all of an operator's calls with one key meet one pause.
var processHolds = newHolds(time.Now)

type holdTransport struct {
	holds *holds
	key   [sha256.Size]byte
	next  http.RoundTripper
}

// RoundTrip answers a call on a paused key with a SlowDown, unsent and unlogged, and sends any other through the call log, so a SlowDown answer's line reads its 503.
func (t *holdTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	probe, err := t.holds.admit(t.key)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	if probe != nil {
		defer t.holds.inconclusive(t.key, probe)
	}
	resp, err := t.next.RoundTrip(req)
	switch {
	case err != nil || resp.StatusCode > http.StatusServiceUnavailable:
		return resp, err
	case resp.StatusCode != http.StatusServiceUnavailable:
		if answersAgain(resp.StatusCode) {
			t.holds.answered(t.key, probe)
		}
		return resp, nil
	}
	slowDown, err := readSlowDown(resp)
	switch {
	case err != nil:
		return nil, fmt.Errorf("the store answered 503 and its answer could not be read: %w", err)
	case !slowDown:
		return resp, nil
	}
	return nil, t.holds.refused(t.key, probe, resp.Header)
}

// answersAgain reports whether status shows the store serving the key again, which no server failure does, nor a 408 or 429, since some stores throttle with those.
func answersAgain(status int) bool {
	return status < http.StatusInternalServerError && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
}

// readSlowDown reads a 503 answer's code, closing its body on a SlowDown and handing the read bytes back to the SDK otherwise.
func readSlowDown(resp *http.Response) (bool, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	_ = resp.Body.Close()
	if err != nil {
		return false, err
	}
	var answer struct {
		Code string `xml:"Code"`
	}
	if xml.Unmarshal(raw, &answer) == nil && strings.HasPrefix(answer.Code, "SlowDown") {
		return true, nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return false, nil
}

func (h *holds) admit(key [sha256.Size]byte) (probe *pause, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.paused[key]
	switch {
	case cur == nil:
		return nil, nil
	case cur.probing || h.now().Before(cur.until):
		return nil, h.slowed(cur.until)
	}
	cur.probing = true
	return cur, nil
}

func (h *holds) answered(key [sha256.Size]byte, probe *pause) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if probe != nil && h.paused[key] == probe {
		delete(h.paused, key)
	}
}

// inconclusive keeps the pause for the next call to test, since neither a lost call nor a server failure says the store answers again, and does nothing once answered has removed or refused has replaced the probe's pause.
func (h *holds) inconclusive(key [sha256.Size]byte, probe *pause) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.paused[key] == probe {
		probe.probing = false
	}
}

// refused pauses key after a SlowDown, keeping a pause another call's SlowDown already started, since a call sent before it could otherwise shorten it.
func (h *holds) refused(key [sha256.Size]byte, probe *pause, header http.Header) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur := h.paused[key]; cur != nil && cur != probe {
		return h.slowed(cur.until)
	}
	streak := 0
	if probe != nil {
		streak = probe.streak + 1
	}
	now := h.now()
	until := now.Add(grow(streak))
	if wait, ok := retryAfter(header); ok {
		until = now.Add(wait)
	}
	h.paused[key] = &pause{until: until, streak: streak}
	return &SlowDown{Until: until}
}

func (h *holds) slowed(until time.Time) *SlowDown {
	if !h.now().Before(until) {
		return &SlowDown{}
	}
	return &SlowDown{Until: until}
}

// grow is firstPause doubled n times, stopping at longestPause so no count overflows.
func grow(n int) time.Duration {
	d := firstPause
	for i := 0; i < n && d < longestPause; i++ {
		d *= 2
	}
	return min(d, longestPause)
}

// retryAfter is the store's Retry-After in seconds, cut to longestPause so a store cannot stop a key's listings for longer.
func retryAfter(header http.Header) (time.Duration, bool) {
	secs, err := strconv.ParseInt(header.Get("Retry-After"), 10, 64)
	if err != nil || secs <= 0 {
		return 0, false
	}
	return time.Duration(min(secs, int64(longestPause/time.Second))) * time.Second, true
}
