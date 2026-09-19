package handler

import (
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

// idempotencyCache remembers the status and response bytes a verb produced
// for a given (subscriptionID, idempotencyKey), so a repeat delivery of the
// same request replays them without calling the Resource again. An empty
// idempotencyKey is never stored here — the caller skips the cache
// entirely for one, since an empty key is never cached.
type idempotencyCache struct {
	mu      sync.Mutex
	entries map[idempotencyKey]idempotencyEntry
}

// newIdempotencyCache returns an empty cache.
func newIdempotencyCache() *idempotencyCache {
	return &idempotencyCache{entries: map[idempotencyKey]idempotencyEntry{}}
}

// get returns the cached entry for key, if any and if it has not expired.
func (c *idempotencyCache) get(now time.Time, key idempotencyKey) (idempotencyEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
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

// put stores status and body for key, and drops every entry older than
// idempotencyTTL — the cache's only bound on how large it grows.
func (c *idempotencyCache) put(now time.Time, key idempotencyKey, status int, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.Sub(e.at) > idempotencyTTL {
			delete(c.entries, k)
		}
	}
	c.entries[key] = idempotencyEntry{status: status, body: body, at: now}
}
