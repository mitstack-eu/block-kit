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
	"github.com/mitstack-eu/block-kit/handler"
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

type limiter struct {
	reset             time.Time
	refusing, failing atomic.Bool
	sent              atomic.Int64
}

func (l *limiter) hook(w http.ResponseWriter, _ *http.Request) bool {
	l.sent.Add(1)
	switch {
	case l.failing.Load():
		cloudtest.Refuse(w, http.StatusServiceUnavailable, "unavailable", "The service is temporarily unavailable.")
	case l.refusing.Load():
		refuseForTheLimit(w, l.reset)
	default:
		return false
	}
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

func newLimited(t *testing.T, reset time.Time) (*limiter, *testClock, *cloud.Client) {
	t.Helper()
	f := cloudtest.New(t, testToken)
	l := &limiter{reset: reset}
	l.refusing.Store(true)
	f.Hook = l.hook
	clock := &testClock{now: refusedAt}
	return l, clock, cloud.NewHolds(clock.Now).Client(f.URL(), testToken)
}

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
		{"a reset already passed by the operator's clock is ignored", refusedAt.Add(-10 * time.Second), []step{
			{"the refusal holds one minute", refusedAt, true, refusedAt.Add(time.Minute), 1},
			{"a call half way through is answered by the hold, unsent", refusedAt.Add(30 * time.Second), true, refusedAt.Add(time.Minute), 1},
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

func TestRateLimited_WaitsUntilItsHoldEnds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reset time.Time
		want  time.Time
	}{
		{"a refusal held for the growing pause waits until the pause ends", time.Time{}, refusedAt.Add(time.Minute)},
		{"a refusal held until Hetzner's reset waits until the reset", refusedAt.Add(20 * time.Second), refusedAt.Add(20 * time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, c := newLimited(t, tc.reset)

			for _, call := range []string{"the refused call", "the call the hold answers unsent"} {
				err := c.KeepPrimaryIPs(ctx, ours)

				var w *handler.Waiting
				if !errors.As(err, &w) || !w.LookAgainAt.Equal(tc.want) {
					t.Errorf("%s = %v with wait %+v; want a wait to look again at %s", call, err, w, tc.want)
				}
			}
		})
	}
}

// stalledHetzner refuses every call for the limit and holds back its answer to call number stalled until release, closing reached once that call arrives.
func stalledHetzner(t *testing.T, stalled int64) (*cloud.Client, *testClock, <-chan struct{}, func()) {
	t.Helper()
	f := cloudtest.New(t, testToken)
	reached, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	t.Cleanup(release)
	var sent atomic.Int64
	f.Hook = func(w http.ResponseWriter, _ *http.Request) bool {
		if sent.Add(1) == stalled {
			close(reached)
			<-released
		}
		refuseForTheLimit(w, time.Time{})
		return true
	}
	clock := &testClock{now: refusedAt}
	return cloud.NewHolds(clock.Now).Client(f.URL(), testToken), clock, reached, release
}

func TestRateLimited_NamesNoEndOnceItsHoldHasPassed(t *testing.T) {
	ended := refusedAt.Add(time.Minute)
	for name, run := range map[string]func(t *testing.T) error{
		"a call arriving while the probe sent after the hold's end has no answer": func(t *testing.T) error {
			c, clock, reached, release := stalledHetzner(t, 2)
			_ = c.KeepPrimaryIPs(ctx, ours)
			clock.set(ended.Add(time.Minute))
			probe := make(chan error, 1)
			go func() { probe <- c.KeepPrimaryIPs(ctx, ours) }()
			<-reached
			defer func() { release(); <-probe }()
			return c.KeepPrimaryIPs(ctx, ours)
		},
		"a refusal answered after the hold another call's refusal started has ended": func(t *testing.T) error {
			c, clock, reached, release := stalledHetzner(t, 1)
			slow := make(chan error, 1)
			go func() { slow <- c.KeepPrimaryIPs(ctx, ours) }()
			<-reached
			_ = c.KeepPrimaryIPs(ctx, ours)
			clock.set(ended.Add(time.Minute))
			release()
			return <-slow
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(t)

			var held *cloud.RateLimited
			var w *handler.Waiting
			if !errors.As(err, &held) || !errors.Is(err, cloud.ErrRateLimited) || !held.Until.IsZero() || !errors.As(err, &w) || !w.LookAgainAt.IsZero() || strings.Contains(err.Error(), "0001") {
				t.Errorf("the call = %v with wait %+v; want a hold naming no end, since the one it met ended at %s, and a message naming no year 0001", err, w, ended)
			}
		})
	}
}

func TestTransport_AServerFailureLeavesTheHoldToDouble(t *testing.T) {
	l, clock, c := newLimited(t, time.Time{})
	_ = c.KeepPrimaryIPs(ctx, ours)
	clock.set(refusedAt.Add(time.Minute))
	l.failing.Store(true)

	failed := c.KeepPrimaryIPs(ctx, ours)
	l.failing.Store(false)
	refused := c.KeepPrimaryIPs(ctx, ours)

	if failed == nil || errors.Is(failed, cloud.ErrRateLimited) {
		t.Errorf("the call meeting the server failure = %v; want that failure", failed)
	}
	if until, held := holdEnd(refused); !held || !until.Equal(refusedAt.Add(3*time.Minute)) {
		t.Errorf("the refusal after the server failure = %v; want the hold doubled to two minutes, ending %s", refused, refusedAt.Add(3*time.Minute))
	}
	if got := l.sent.Load(); got != 3 {
		t.Errorf("%d calls reached Hetzner; want 3", got)
	}
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransport_ACallThatPanicsLeavesTheHoldForTheNextCallToTest(t *testing.T) {
	clock := &testClock{now: refusedAt}
	answer, sent := http.StatusTooManyRequests, 0
	rt := cloud.HoldTransport(cloud.NewHolds(clock.Now), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent++
		if answer == 0 {
			panic("the transport failed mid-call")
		}
		return &http.Response{StatusCode: answer, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	}))
	call := func() (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://cloud.test/v1/primary_ips", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		return rt.RoundTrip(req)
	}
	_, refused := call()
	clock.set(refusedAt.Add(time.Minute))
	answer = 0
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		_, _ = call()
	}()
	answer = http.StatusOK

	resp, err := call()

	if _, held := holdEnd(refused); !held || panicked == nil {
		t.Fatalf("the first call = %v and the probe panicked with %v; want the hold, then the probe's panic reaching its caller", refused, panicked)
	}
	if err != nil || resp == nil || resp.StatusCode != http.StatusOK || sent != 3 {
		t.Errorf("the call after the panicking probe = %v, %v with %d calls sent; want it sent as the next probe and answered 200, 3 calls sent", resp, err, sent)
	}
}

var processRuns atomic.Int64

func TestNewAtAndHTTPClient_ShareTheOperatorsHolds(t *testing.T) {
	// The package's holds outlive one run of this test, so each run takes a token no earlier run held.
	sharedToken := "process-holds-token-" + strconv.FormatInt(processRuns.Add(1), 10)
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
