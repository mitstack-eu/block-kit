// Package callhold holds every call made with one credential after a service refuses one for sending too fast, until a pause ends, and then lets one call out to test whether the refusals have stopped.
package callhold

import (
	"crypto/sha256"
	"sync"
	"time"
)

type Key = [sha256.Size]byte

func KeyOf(credential string) Key { return sha256.Sum256([]byte(credential)) }

// Holds keeps each key by the credential's SHA-256 in the process's memory, so a restarted process learns of a limit again from its first refused call.
type Holds struct {
	now            func() time.Time
	first, longest time.Duration
	mu             sync.Mutex
	held           map[Key]*hold
}

type hold struct {
	until   time.Time
	streak  int
	probing bool
}

// New holds a key for first after a refusal, doubled by each refusal in a row up to longest.
func New(now func() time.Time, first, longest time.Duration) *Holds {
	return &Holds{now: now, first: first, longest: longest, held: map[Key]*hold{}}
}

type Call struct {
	holds *Holds
	key   Key
	probe *hold
}

// Admit lets a call on key out, or returns false with the hold's end, which is the zero time once the hold has ended while the call sent to test it has no answer yet.
func (h *Holds) Admit(key Key) (Call, time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.held[key]
	switch {
	case cur == nil:
		return Call{holds: h, key: key}, time.Time{}, true
	case cur.probing || h.now().Before(cur.until):
		return Call{}, h.ending(cur.until), false
	}
	cur.probing = true
	return Call{holds: h, key: key, probe: cur}, time.Time{}, true
}

func (c Call) Answered() {
	if c.probe == nil {
		return
	}
	c.holds.mu.Lock()
	defer c.holds.mu.Unlock()
	if c.holds.held[c.key] == c.probe {
		delete(c.holds.held, c.key)
	}
}

// Inconclusive keeps the hold for the next call to test, since neither a lost call nor a server failure says the limit has passed, and does nothing once Answered has removed or Refused has replaced the call's hold.
func (c Call) Inconclusive() {
	if c.probe == nil {
		return
	}
	c.holds.mu.Lock()
	defer c.holds.mu.Unlock()
	if c.holds.held[c.key] == c.probe {
		c.probe.probing = false
	}
}

// Refused holds the key for the pause its streak reaches, ending where adjust moves that end, and returns the end, keeping instead a hold another call's refusal already started, since a call sent before that refusal could otherwise shorten it.
func (c Call) Refused(adjust func(now, until time.Time) time.Time) time.Time {
	h := c.holds
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur := h.held[c.key]; cur != nil && cur != c.probe {
		return h.ending(cur.until)
	}
	streak := 0
	if c.probe != nil {
		streak = c.probe.streak + 1
	}
	now := h.now()
	until := adjust(now, now.Add(Grow(h.first, h.longest, streak)))
	h.held[c.key] = &hold{until: until, streak: streak}
	return until
}

func (h *Holds) ending(until time.Time) time.Time {
	if !h.now().Before(until) {
		return time.Time{}
	}
	return until
}

// Grow is first doubled n times, stopping at ceiling so no count overflows.
func Grow(first, ceiling time.Duration, n int) time.Duration {
	d := first
	for i := 0; i < n && d < ceiling; i++ {
		d *= 2
	}
	return min(d, ceiling)
}
