package s3store

import (
	"net/http"
	"time"
)

type Holds = holds

const (
	FirstPause   = firstPause
	LongestPause = longestPause
)

func NewHolds(now func() time.Time) *Holds { return newHolds(now) }

// NewThrough is New with its own holds and a base transport that trusts a test server's certificate.
func NewThrough(cfg Config, h *Holds, base http.RoundTripper) (*Client, error) {
	return newClient(cfg, h, base)
}
