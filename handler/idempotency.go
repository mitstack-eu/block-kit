package handler

import (
	"net/http"
	"sync"
	"time"
)

// idempotencyTTL bounds the cache without a separate size limit.
const idempotencyTTL = time.Hour

// idempotencyKey pairs idempotencyKey with subscriptionID so two subscriptions reusing a key never replay each other's response, and carries the named resource so one key naming different resources never replays one's answer for another.
type idempotencyKey struct {
	subscriptionID string
	idempotencyKey string
	resource       string
}

type idempotencyEntry struct {
	status int
	body   []byte
	at     time.Time
}

// isCacheableStatus keeps a 500 retryable, since the engine keeps one IdempotencyKey across every retry and a cached 500 would turn one transient error into a permanent one.
func isCacheableStatus(status int) bool {
	return (status >= 200 && status < 300) || status == http.StatusBadRequest
}

// inflightCall releases wg exactly once however run exits, so a waiter never blocks forever; only an escaped panic leaves it holding a zero status.
type inflightCall struct {
	wg      sync.WaitGroup
	status  int
	body    []byte
	settled bool
}

// idempotencyCache is single-flight and stores only a settled answer, since a stored pending or absent:false answer would replay "not yet" for the whole TTL instead of observing the resource.
//
// An empty idempotencyKey and a validate never reach it, since a probe must observe the current state.
type idempotencyCache struct {
	mu      sync.Mutex
	entries map[idempotencyKey]idempotencyEntry
	calls   map[idempotencyKey]*inflightCall
}

func newIdempotencyCache() *idempotencyCache {
	return &idempotencyCache{
		entries: map[idempotencyKey]idempotencyEntry{},
		calls:   map[idempotencyKey]*inflightCall{},
	}
}

// do leaves neither a cache entry nor an in-flight call behind for an answer it does not store, so the next delivery runs run again.
func (c *idempotencyCache) do(now time.Time, key idempotencyKey, run func() (status int, body []byte, settled bool)) (int, []byte) {
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
		if call.settled && isCacheableStatus(call.status) {
			c.putLocked(now, key, call.status, call.body)
		}
		c.mu.Unlock()
		call.wg.Done()
	}()

	call.status, call.body, call.settled = run()
	return call.status, call.body
}

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

// putLocked drops every entry older than idempotencyTTL, the cache's only bound on growth.
func (c *idempotencyCache) putLocked(now time.Time, key idempotencyKey, status int, body []byte) {
	for k, e := range c.entries {
		if now.Sub(e.at) > idempotencyTTL {
			delete(c.entries, k)
		}
	}
	c.entries[key] = idempotencyEntry{status: status, body: body, at: now}
}
