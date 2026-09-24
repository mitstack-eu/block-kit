package handler

import (
	"net/http"
	"sync"
	"time"
)

// idempotencyTTL is how long a cached response is replayed before it is
// dropped, bounding the cache without a separate size limit.
const idempotencyTTL = time.Hour

// idempotencyKey identifies a cached response. It pairs idempotencyKey with
// subscriptionID rather than keying on idempotencyKey alone, so two
// different subscriptions that happen to reuse the same key never replay
// each other's response — a multi-tenancy requirement, not an edge case.
type idempotencyKey struct {
	subscriptionID string
	idempotencyKey string
}

// idempotencyEntry is one cached response, timestamped so it can be dropped
// once idempotencyTTL has passed.
type idempotencyEntry struct {
	status int
	body   []byte
	at     time.Time
}

// isCacheableStatus reports whether status is a deterministic outcome worth
// replaying: a 2xx, or the 400 a *Refused produces. A 500 stays retryable —
// the engine keeps one IdempotencyKey across every retry of a call, so
// caching a 500 under that key would make every later retry replay the
// stale failure instead of ever reaching the Resource again, turning one
// transient error into a permanent one.
func isCacheableStatus(status int) bool {
	return (status >= 200 && status < 300) || status == http.StatusBadRequest
}

// inflightCall tracks one delivery that is still running a verb for a given
// key, so a concurrent delivery of the same key can wait for it instead of
// calling the Resource a second time. The run function recovers a panic in
// the Resource or a hook itself, so a waiter on a call that panicked
// receives the same 500 and body the calling delivery answers. wg is still
// released exactly once however run exits, including by a panic that
// escapes it, so a waiter never blocks forever; only such an escaped panic
// leaves the waiter holding a zero status.
type inflightCall struct {
	wg     sync.WaitGroup
	status int
	body   []byte
}

// idempotencyCache remembers the status and response bytes a verb produced
// for a given (subscriptionID, idempotencyKey), so a repeat delivery of the
// same request replays them without calling the Resource again, and makes
// that behaviour single-flight: an overlapping delivery of a key still in
// flight waits for the first to finish and receives its exact response. An
// empty idempotencyKey is never stored here — the caller skips the cache
// entirely for one, since an empty key is never cached.
type idempotencyCache struct {
	mu      sync.Mutex
	entries map[idempotencyKey]idempotencyEntry
	calls   map[idempotencyKey]*inflightCall
}

// newIdempotencyCache returns an empty cache.
func newIdempotencyCache() *idempotencyCache {
	return &idempotencyCache{
		entries: map[idempotencyKey]idempotencyEntry{},
		calls:   map[idempotencyKey]*inflightCall{},
	}
}

// do returns the cached response for key when one exists, joins an
// in-flight call for key when one is already running, or else calls run
// itself, storing its result only when isCacheableStatus accepts the
// status it produced. A key whose call ends in a status the cache does not
// store leaves neither a cache entry nor an in-flight call behind, so the
// next delivery of that key runs run again rather than joining a stale
// call or replaying a response that was never stored.
func (c *idempotencyCache) do(now time.Time, key idempotencyKey, run func() (int, []byte)) (int, []byte) {
	c.mu.Lock()
	if entry, ok := c.getLocked(now, key); ok {
		c.mu.Unlock()
		return entry.status, entry.body
	}
	if call, ok := c.calls[key]; ok {
		c.mu.Unlock()
		call.wg.Wait()
		return call.status, call.body
	}

	call := &inflightCall{}
	call.wg.Add(1)
	c.calls[key] = call
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.calls, key)
		if isCacheableStatus(call.status) {
			c.putLocked(now, key, call.status, call.body)
		}
		c.mu.Unlock()
		call.wg.Done()
	}()

	call.status, call.body = run()
	return call.status, call.body
}

// getLocked returns the cached entry for key, if any and if it has not
// expired. The caller holds c.mu.
func (c *idempotencyCache) getLocked(now time.Time, key idempotencyKey) (idempotencyEntry, bool) {
	entry, ok := c.entries[key]
	if !ok {
		return idempotencyEntry{}, false
	}
	if now.Sub(entry.at) > idempotencyTTL {
		delete(c.entries, key)
		return idempotencyEntry{}, false
	}
	return entry, true
}

// putLocked stores status and body for key, and drops every entry older
// than idempotencyTTL — the cache's only bound on how large it grows. The
// caller holds c.mu.
func (c *idempotencyCache) putLocked(now time.Time, key idempotencyKey, status int, body []byte) {
	for k, e := range c.entries {
		if now.Sub(e.at) > idempotencyTTL {
			delete(c.entries, k)
		}
	}
	c.entries[key] = idempotencyEntry{status: status, body: body, at: now}
}
