package handler

import (
	"sync"
	"time"
)

// fullReadEvery gives four full reads a day; between them a health check is a probe.
const fullReadEvery = 6 * time.Hour

type fullReadKey struct {
	subscriptionID string
	resource       string
}

// fullReadClock holds in memory when each resource was last fully read, so a restart or a second replica, holding no time, reads the account more often and never less.
type fullReadClock struct {
	mu   sync.Mutex
	last map[fullReadKey]time.Time
}

func newFullReadClock() *fullReadClock {
	return &fullReadClock{last: map[fullReadKey]time.Time{}}
}

func (c *fullReadClock) due(key fullReadKey, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.last[key]
	return !ok || now.Sub(at) >= fullReadEvery
}

// record drops every time already older than fullReadEvery, the clock's only bound on growth, since a dropped time only makes the next check a full read.
func (c *fullReadClock) record(key fullReadKey, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, at := range c.last {
		if now.Sub(at) >= fullReadEvery {
			delete(c.last, k)
		}
	}
	c.last[key] = now
}
