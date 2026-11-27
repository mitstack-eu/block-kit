package s3store

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/callhold"
)

// ErrSlowDown matches the error of a call the store answered 503 SlowDown, and of every call held unsent on that key after it.
var ErrSlowDown = errors.New("the S3-compatible store asked this key's calls to slow down")

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

type holds = callhold.Holds

func newHolds(now func() time.Time) *holds { return callhold.New(now, firstPause, longestPause) }

// processHolds is shared by every client New returns, so all of an operator's calls with one key meet one pause.
var processHolds = newHolds(time.Now)

type holdTransport struct {
	holds *holds
	key   callhold.Key
	next  http.RoundTripper
}

// RoundTrip answers a call on a paused key with a SlowDown, unsent and unlogged, and sends any other through the call log, so a SlowDown answer's line reads its 503.
func (t *holdTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	call, until, ok := t.holds.Admit(t.key)
	if !ok {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, &SlowDown{Until: until}
	}
	defer call.Inconclusive()
	resp, err := t.next.RoundTrip(req)
	switch {
	case err != nil || resp.StatusCode > http.StatusServiceUnavailable:
		return resp, err
	case resp.StatusCode != http.StatusServiceUnavailable:
		if answersAgain(resp.StatusCode) {
			call.Answered()
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
	return nil, &SlowDown{Until: call.Refused(func(now, until time.Time) time.Time {
		if wait, ok := retryAfter(resp.Header); ok {
			return now.Add(wait)
		}
		return until
	})}
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

// retryAfter is the store's Retry-After in seconds, cut to longestPause so a store cannot stop a key's listings for longer.
func retryAfter(header http.Header) (time.Duration, bool) {
	secs, err := strconv.ParseInt(header.Get("Retry-After"), 10, 64)
	if err != nil || secs <= 0 {
		return 0, false
	}
	return time.Duration(min(secs, int64(longestPause/time.Second))) * time.Second, true
}
