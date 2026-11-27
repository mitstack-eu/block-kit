package callhold_test

// A refusal holds its key's calls until a pause ends, then lets one call out to test it, and the pause doubles while refusals continue.

import (
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/callhold"
)

var refusedAt = time.Date(2026, time.November, 27, 9, 0, 0, 0, time.UTC)

const first, longest = time.Minute, 4 * time.Minute

var key, otherKey = callhold.KeyOf("token-one"), callhold.KeyOf("token-two")

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func keep(_, until time.Time) time.Time { return until }

// refusedOnce returns holds whose key a refusal at refusedAt holds until refusedAt plus first.
func refusedOnce(t *testing.T) (*callhold.Holds, *clock) {
	t.Helper()
	c := &clock{now: refusedAt}
	h := callhold.New(c.Now, first, longest)
	call, _, _ := h.Admit(key)
	if until := call.Refused(keep); !until.Equal(refusedAt.Add(first)) {
		t.Fatalf("a first refusal holds the key until %v; want %v", until, refusedAt.Add(first))
	}
	return h, c
}

func admitted(t *testing.T, h *callhold.Holds, k callhold.Key, wantOK bool, wantUntil time.Time) callhold.Call {
	t.Helper()
	call, until, ok := h.Admit(k)
	if ok != wantOK || !until.Equal(wantUntil) {
		t.Fatalf("Admit = %v, %v; want %v, %v", ok, until, wantOK, wantUntil)
	}
	return call
}

func TestAdmit_AHeldKeyWaitsUntilThePauseEndsWhileAnotherKeyGoesOut(t *testing.T) {
	h, c := refusedOnce(t)
	c.now = refusedAt.Add(first - time.Second)

	admitted(t, h, key, false, refusedAt.Add(first))
	admitted(t, h, otherKey, true, time.Time{})
}

func TestAdmit_OnceThePauseEndsOneCallGoesOutAndTheNextWaitsOnItsAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(callhold.Call)
	}{
		{"an answered call ends the hold", callhold.Call.Answered},
		{"an inconclusive call leaves the next call to test the hold", callhold.Call.Inconclusive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := refusedOnce(t)
			c.now = refusedAt.Add(first)

			probe := admitted(t, h, key, true, time.Time{})
			admitted(t, h, key, false, time.Time{})
			tc.answer(probe)
			next := admitted(t, h, key, true, time.Time{})
			next.Answered()
			admitted(t, h, key, true, time.Time{})
		})
	}
}

func TestRefused_EachRefusalInARowDoublesThePauseUpToTheLongest(t *testing.T) {
	h, c := refusedOnce(t)
	for _, pause := range []time.Duration{2 * time.Minute, longest, longest} {
		c.now = c.now.Add(longest)
		probe := admitted(t, h, key, true, time.Time{})
		if until := probe.Refused(keep); !until.Equal(c.now.Add(pause)) {
			t.Fatalf("a refusal in a row holds the key until %v; want %v", until, c.now.Add(pause))
		}
	}
}

func TestRefused_ACallSentBeforeAnotherCallsRefusalKeepsThatHold(t *testing.T) {
	c := &clock{now: refusedAt}
	h := callhold.New(c.Now, first, longest)
	early, _, _ := h.Admit(key)
	late, _, _ := h.Admit(key)
	late.Refused(keep)
	c.now = refusedAt.Add(time.Second)

	if until := early.Refused(keep); !until.Equal(refusedAt.Add(first)) {
		t.Errorf("the second refusal returns %v; want the first hold's end %v", until, refusedAt.Add(first))
	}
	c.now = refusedAt.Add(first)
	if until := early.Refused(keep); !until.IsZero() {
		t.Errorf("a refusal answered after that hold ended returns %v; want the zero time", until)
	}
}

func TestRefused_AdjustSetsWhenTheHoldEnds(t *testing.T) {
	c := &clock{now: refusedAt}
	h := callhold.New(c.Now, first, longest)
	call, _, _ := h.Admit(key)
	sooner := refusedAt.Add(10 * time.Second)

	if until := call.Refused(func(time.Time, time.Time) time.Time { return sooner }); !until.Equal(sooner) {
		t.Fatalf("Refused = %v; want %v", until, sooner)
	}
	admitted(t, h, key, false, sooner)
}

func TestGrow_DoublesFirstNTimesAndStopsAtTheCeiling(t *testing.T) {
	for n, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second} {
		if got := callhold.Grow(time.Second, 5*time.Second, n); got != want {
			t.Errorf("Grow(1s, 5s, %d) = %v; want %v", n, got, want)
		}
	}
	if got := callhold.Grow(time.Second, 5*time.Second, 1<<40); got != 5*time.Second {
		t.Errorf("Grow with a huge count = %v; want the ceiling", got)
	}
}
