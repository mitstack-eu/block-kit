package s3store_test

// A 503 SlowDown answer pauses every call made with that key until the pause ends, which doubles while SlowDown answers continue and ends sooner when the store names a Retry-After, and the caller is told when it ends.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/s3store"
	"github.com/mitstack-eu/block-kit/handler"
)

var slowedAt = time.Date(2026, time.November, 27, 9, 0, 0, 0, time.UTC)

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

// slowStore answers SlowDown while slowing is set, naming retryAfter seconds when it is above zero.
func slowStore(t *testing.T, retryAfter int) (*store, *atomic.Bool) {
	t.Helper()
	s := newStore(t)
	fillPrefix(s, "db-1/wal/", 3)
	var slowing atomic.Bool
	s.refuseWith(func(w http.ResponseWriter) bool {
		if !slowing.Load() {
			return false
		}
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		}
		answerError(w, http.StatusServiceUnavailable, "SlowDown", "Please reduce your request rate.")
		return true
	})
	return s, &slowing
}

func clientWithKey(t *testing.T, s *store, holds *s3store.Holds, accessKey string) *s3store.Client {
	t.Helper()
	cfg := testConfig(s)
	cfg.AccessKey = accessKey
	c, err := s3store.NewThrough(cfg, holds, s.srv.Client().Transport)
	if err != nil {
		t.Fatalf("NewThrough(%s) = %v; want a client", s.srv.URL, err)
	}
	return c
}

// pauseEnd is when the pause err reports ends, as its SlowDown and the handler.Waiting the kit's handler answers with both say, and false when err is no pause.
func pauseEnd(t *testing.T, err error) (time.Time, bool) {
	t.Helper()
	var slow *s3store.SlowDown
	if !errors.As(err, &slow) || !errors.Is(err, s3store.ErrSlowDown) {
		return time.Time{}, false
	}
	var waiting *handler.Waiting
	if !errors.As(err, &waiting) || !waiting.LookAgainAt.Equal(slow.Until) {
		t.Errorf("pause %v tells the handler %v; want a handler.Waiting looking again at %s", err, waiting, slow.Until)
	}
	return slow.Until, true
}

// pauseStep is one list call at a time on the test clock, answered by a pause ending at paused, or sent and answered when paused is zero, with sent calls reaching the store so far.
type pauseStep struct {
	name    string
	at      time.Time
	slowing bool
	client  *s3store.Client
	paused  time.Time
	sent    int64
}

func runPauseSteps(t *testing.T, s *store, slowing *atomic.Bool, clock *testClock, steps []pauseStep) {
	t.Helper()
	for _, step := range steps {
		clock.set(step.at)
		slowing.Store(step.slowing)

		_, err := step.client.List(context.Background(), s3store.Query{Prefix: "db-1/wal/"})

		until, paused := pauseEnd(t, err)
		switch {
		case step.paused.IsZero() && err != nil:
			t.Errorf("%s: List = %v; want it sent and answered", step.name, err)
		case !step.paused.IsZero() && (!paused || !until.Equal(step.paused)):
			t.Errorf("%s: List = %v; want a pause ending %s", step.name, err, step.paused)
		}
		if err != nil {
			assertNoKey(t, step.name+": the error", err.Error())
		}
		if got := s.sent.Load(); got != step.sent {
			t.Errorf("%s: %d calls reached the store; want %d", step.name, got, step.sent)
		}
	}
}

func TestList_AStoreThatAnswers503SlowDownHoldsTheNextCallWithTheSameKeyUntilThePauseEnds(t *testing.T) {
	s, slowing := slowStore(t, 0)
	clock := &testClock{now: slowedAt}
	holds := s3store.NewHolds(clock.Now)
	c, sameKey, otherKey := newClient(t, s, holds), newClient(t, s, holds), clientWithKey(t, s, holds, otherAccessKey)
	first := slowedAt.Add(s3store.FirstPause)
	second := first.Add(2 * s3store.FirstPause)

	runPauseSteps(t, s, slowing, clock, []pauseStep{
		{"the first call meets SlowDown and pauses its key", slowedAt, true, c, first, 1},
		{"a second call at once waits for the pause, unsent", slowedAt, true, c, first, 1},
		{"another client with the same key waits for the same pause, unsent", slowedAt, true, sameKey, first, 1},
		{"a call a second before the end waits, unsent", first.Add(-time.Second), true, c, first, 1},
		{"a call with another key goes out during the pause", first.Add(-time.Second), false, otherKey, time.Time{}, 2},
		{"the call once the pause ends goes out, meets SlowDown again and pauses twice as long", first, true, c, second, 3},
		{"the call once the second pause ends goes out and is answered", second, false, c, time.Time{}, 4},
		{"the call after that answer goes out with no pause", second, false, c, time.Time{}, 5},
	})
}

func TestList_ASlowDownNamingRetryAfterEndsThePauseThen(t *testing.T) {
	s, slowing := slowStore(t, 3)
	clock := &testClock{now: slowedAt}
	c := newClient(t, s, s3store.NewHolds(clock.Now))
	retryAt := slowedAt.Add(3 * time.Second)

	runPauseSteps(t, s, slowing, clock, []pauseStep{
		{"a SlowDown naming Retry-After 3 pauses the key three seconds", slowedAt, true, c, retryAt, 1},
		{"the call at the Retry-After time goes out and is answered", retryAt, false, c, time.Time{}, 2},
	})
}
