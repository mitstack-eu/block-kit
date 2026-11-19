package cloud_test

// A rate-limit refusal holds its token's calls for a pause that doubles while refusals continue and ends by Hetzner's reset time, and the first call after it is the only one sent.

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud/cloudtest"
)

var refusedAt = time.Date(2026, time.November, 19, 20, 0, 0, 0, time.UTC)

// testClock is the holds' clock, which a test moves by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// limiter counts every call that reaches the fake and refuses each for the rate limit while refusing is set, naming reset unless it is zero.
type limiter struct {
	reset    time.Time
	refusing atomic.Bool
	sent     atomic.Int64
}

func (l *limiter) hook(w http.ResponseWriter, _ *http.Request) bool {
	l.sent.Add(1)
	if !l.refusing.Load() {
		return false
	}
	refuseForTheLimit(w, l.reset)
	return true
}

// refuseForTheLimit answers as Hetzner answers a call past its rate limit.
func refuseForTheLimit(w http.ResponseWriter, reset time.Time) {
	w.Header().Set("RateLimit-Limit", "3600")
	w.Header().Set("RateLimit-Remaining", "0")
	if !reset.IsZero() {
		w.Header().Set("RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	}
	cloudtest.Refuse(w, http.StatusTooManyRequests, "rate_limit_exceeded", "Limit of '5000' requests per hour for ip '192.0.2.10' reached.")
}

// newLimited serves a fake that refuses every call until the test says otherwise, to a client whose holds read clock.
func newLimited(t *testing.T, reset time.Time) (*limiter, *testClock, *cloud.Client) {
	t.Helper()
	f := cloudtest.New(t, testToken)
	l := &limiter{reset: reset}
	l.refusing.Store(true)
	f.Hook = l.hook
	clock := &testClock{now: refusedAt}
	return l, clock, cloud.NewHolds(clock.Now).Client(f.URL(), testToken)
}

// holdEnd is when the hold err names ends, and false when err is no hold.
func holdEnd(err error) (time.Time, bool) {
	var held *cloud.RateLimited
	if !errors.As(err, &held) || !errors.Is(err, cloud.ErrRateLimited) {
		return time.Time{}, false
	}
	return held.Until, true
}

// step is one call at a time on the test clock, answered by a hold ending at held, or sent and answered when held is zero, with sent calls reaching Hetzner so far.
type step struct {
	name     string
	at       time.Time
	refusing bool
	held     time.Time
	sent     int64
}

func runSteps(t *testing.T, l *limiter, clock *testClock, c *cloud.Client, steps []step) {
	t.Helper()
	for _, s := range steps {
		clock.set(s.at)
		l.refusing.Store(s.refusing)

		err := c.KeepPrimaryIPs(ctx, ours)

		until, held := holdEnd(err)
		switch {
		case s.held.IsZero() && err != nil:
			t.Errorf("%s: call = %v; want it sent and answered", s.name, err)
		case !s.held.IsZero() && (!held || !until.Equal(s.held)):
			t.Errorf("%s: call = %v; want a hold ending %s", s.name, err, s.held)
		case err != nil && strings.Contains(err.Error(), testToken):
			t.Errorf("%s: call = %v; want an error that does not carry the token", s.name, err)
		}
		if got := l.sent.Load(); got != s.sent {
			t.Errorf("%s: %d calls reached Hetzner; want %d", s.name, got, s.sent)
		}
	}
}

func TestTransport_HoldsCallsForAGrowingPause(t *testing.T) {
	l, clock, c := newLimited(t, refusedAt.Add(30*time.Minute))
	first, second := refusedAt.Add(time.Minute), refusedAt.Add(3*time.Minute)

	runSteps(t, l, clock, c, []step{
		{"the first call meets the refusal and holds the token one minute", refusedAt, true, first, 1},
		{"a call at once is answered by the hold, unsent", refusedAt, true, first, 1},
		{"a call half way through is answered by the hold, unsent", refusedAt.Add(30 * time.Second), true, first, 1},
		{"a call a second before the end is answered by the hold, unsent", first.Add(-time.Second), true, first, 1},
		{"the call once the hold ends goes out, meets a second refusal and holds two minutes", first, true, second, 2},
		{"a call during the second hold is answered by it, unsent", first.Add(time.Minute), true, second, 2},
		{"the call once the second hold ends goes out and succeeds", second, false, time.Time{}, 3},
		{"the call after the success goes out with no hold", second, false, time.Time{}, 4},
		{"a refusal after the success holds one minute again", second, true, second.Add(time.Minute), 5},
	})
}

func TestTransport_HoldEndsByRateLimitReset(t *testing.T) {
	cases := []struct {
		name  string
		reset time.Time
		steps []step
	}{
		{"a reset 20 seconds ahead ends the first hold then", refusedAt.Add(20 * time.Second), []step{
			{"the refusal holds until the reset", refusedAt, true, refusedAt.Add(20 * time.Second), 1},
			{"the call at the reset goes out and succeeds", refusedAt.Add(20 * time.Second), false, time.Time{}, 2},
		}},
		{"a reset sooner than the doubled hold ends the second hold then", refusedAt.Add(2 * time.Minute), []step{
			{"the refusal holds one minute", refusedAt, true, refusedAt.Add(time.Minute), 1},
			{"the second refusal holds until the reset, not two minutes", refusedAt.Add(time.Minute), true, refusedAt.Add(2 * time.Minute), 2},
			{"the call at the reset goes out", refusedAt.Add(2 * time.Minute), false, time.Time{}, 3},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, clock, c := newLimited(t, tc.reset)

			runSteps(t, l, clock, c, tc.steps)
		})
	}
}

func TestTransport_RefusalWithoutResetTakesTheGrowingHold(t *testing.T) {
	l, clock, c := newLimited(t, time.Time{})
	lengths := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour}

	var steps []step
	at := refusedAt
	for i, length := range lengths {
		steps = append(steps, step{"refusal " + strconv.Itoa(i+1) + " in a row holds " + length.String(), at, true, at.Add(length), int64(i + 1)})
		at = at.Add(length)
	}

	runSteps(t, l, clock, c, steps)
}

// The cloudtest fake serves one token, and this test needs two on one host, so a hold keyed by anything but the token fails it.
func TestTransport_HoldsOnlyTheRefusedToken(t *testing.T) {
	var mu sync.Mutex
	sent := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent[r.Header.Get("Authorization")]++
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer org-a-token" {
			refuseForTheLimit(w, refusedAt.Add(30*time.Minute))
			return
		}
		_, _ = w.Write([]byte(`{"primary_ips":[],"meta":{"pagination":{"next_page":null}}}`))
	}))
	t.Cleanup(srv.Close)
	holds := cloud.NewHolds((&testClock{now: refusedAt}).Now)
	orgA, orgB := holds.Client(srv.URL+"/v1", "org-a-token"), holds.Client(srv.URL+"/v1", "org-b-token")

	errsA := []error{orgA.KeepPrimaryIPs(ctx, ours), orgA.KeepPrimaryIPs(ctx, ours)}
	errsB := []error{orgB.KeepPrimaryIPs(ctx, theirs), orgB.KeepPrimaryIPs(ctx, theirs), orgB.KeepPrimaryIPs(ctx, theirs)}

	for i, err := range errsA {
		if _, held := holdEnd(err); !held {
			t.Errorf("org A's call %d = %v; want the hold", i+1, err)
		}
	}
	for i, err := range errsB {
		if err != nil {
			t.Errorf("org B's call %d = %v; want it sent and answered", i+1, err)
		}
	}
	if want := map[string]int{"Bearer org-a-token": 1, "Bearer org-b-token": 3}; sent["Bearer org-a-token"] != 1 || sent["Bearer org-b-token"] != 3 {
		t.Errorf("calls reaching Hetzner per token = %v; want %v", sent, want)
	}
}

func TestTransport_RefusalIsNoTimeout(t *testing.T) {
	f := cloudtest.New(t, testToken)
	l := &limiter{reset: refusedAt.Add(30 * time.Minute)}
	l.refusing.Store(true)
	f.Hook = l.hook
	client := cloud.NewHolds((&testClock{now: refusedAt}).Now).HTTPClient()

	for _, call := range []string{"the refused call", "the held call after it"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.URL()+"/primary_ips", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)

		resp, err := client.Do(req)

		if resp != nil {
			resp.Body.Close()
			t.Errorf("%s answered %d; want no response beside the error", call, resp.StatusCode)
		}
		var urlErr *url.Error
		if !errors.As(err, &urlErr) || !errors.Is(err, cloud.ErrRateLimited) {
			t.Errorf("%s = %v; want a *url.Error wrapping the hold", call, err)
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Errorf("%s = %v reads as a timeout, which hcloud-go's retry policy sends again", call, err)
		}
	}
	if sent := l.sent.Load(); sent != 1 {
		t.Errorf("%d calls reached Hetzner; want 1", sent)
	}
}

func TestNewAtAndHTTPClient_ShareTheOperatorsHolds(t *testing.T) {
	const sharedToken = "process-holds-token"
	f := cloudtest.New(t, sharedToken)
	l := &limiter{reset: time.Now().Add(30 * time.Minute)}
	l.refusing.Store(true)
	f.Hook = l.hook

	refused := cloud.NewAt(f.URL(), sharedToken).KeepPrimaryIPs(ctx, ours)
	another := cloud.NewAt(f.URL(), sharedToken).KeepPrimaryIPs(ctx, ours)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.URL()+"/primary_ips", nil)
	req.Header.Set("Authorization", "Bearer "+sharedToken)
	resp, viaHTTPClient := cloud.HTTPClient().Do(req)

	if resp != nil {
		resp.Body.Close()
	}
	for name, err := range map[string]error{"the refused call": refused, "a second client's call": another, "hcloud-go's client's call": viaHTTPClient} {
		if _, held := holdEnd(err); !held {
			t.Errorf("%s = %v; want the hold", name, err)
		}
	}
	if sent := l.sent.Load(); sent != 1 {
		t.Errorf("%d calls reached Hetzner; want 1, the others answered by the one hold", sent)
	}
}
