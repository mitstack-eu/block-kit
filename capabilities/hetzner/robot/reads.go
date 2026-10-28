package robot

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ReadWindow keeps each read at 60 an hour, inside the lowest limit Robot documents, 200 an hour for GET /server/{number}.
const ReadWindow = time.Minute

const maxRemembered = 1024

// answers is shared by every client in the process, since a block builds a client for each call it serves.
var answers = struct {
	sync.Mutex
	byKey map[answerKey]answer
	// writes counts every write, so a read that a write overtook does not store the state from before it.
	writes uint64
}{byKey: map[answerKey]answer{}}

// answerKey holds the login as a hash of the endpoint, user and password, so no other login is answered from it.
type answerKey struct {
	login [sha256.Size]byte
	path  string
}

type answer struct {
	raw     []byte
	err     error
	expires time.Time
}

type freshRead struct{}

// afresh sends each read to Robot, since a call that writes never acts on a remembered answer nor waits on one.
func afresh(ctx context.Context) context.Context { return context.WithValue(ctx, freshRead{}, true) }

// exchange answers a read from the window, keeping an answer or a rate-limit refusal, and drops what a write changes.
func (c *Client) exchange(ctx context.Context, method, path string, form url.Values) ([]byte, error) {
	if method != http.MethodGet {
		defer c.forget(path)
		return c.send(ctx, method, path, form)
	}
	if !remembered(path) {
		return c.send(ctx, method, path, form)
	}
	key := answerKey{login: c.login, path: path}
	answers.Lock()
	a, ok := answers.byKey[key]
	writes := answers.writes
	answers.Unlock()
	if ok && ctx.Value(freshRead{}) == nil && c.now().Before(a.expires) {
		return a.raw, a.err
	}
	raw, err := c.send(ctx, method, path, form)
	if c.ReadWindow > 0 && (err == nil || errors.Is(err, ErrRateLimited)) {
		c.remember(key, writes, answer{raw: raw, err: err, expires: c.now().Add(c.ReadWindow)})
	}
	return raw, err
}

func (c *Client) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *Client) remember(key answerKey, writes uint64, a answer) {
	answers.Lock()
	defer answers.Unlock()
	if answers.writes != writes {
		return
	}
	if len(answers.byKey) >= maxRemembered {
		now := c.now()
		for k, old := range answers.byKey {
			if !now.Before(old.expires) {
				delete(answers.byKey, k)
			}
		}
	}
	answers.byKey[key] = a
}

// forget drops the remembered reads of the resource a write to path changes, and of its kind's list.
func (c *Client) forget(path string) {
	kind, rest, _ := strings.Cut(strings.Trim(path, "/"), "/")
	id, _, _ := strings.Cut(rest, "/")
	answers.Lock()
	defer answers.Unlock()
	answers.writes++
	delete(answers.byKey, answerKey{login: c.login, path: "/" + kind})
	delete(answers.byKey, answerKey{login: c.login, path: "/" + kind + "/" + id})
}

func remembered(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch len(parts) {
	case 1:
		return parts[0] == "vswitch"
	case 2:
		return parts[0] == "server" || parts[0] == "vswitch" || parts[0] == "firewall"
	}
	return false
}
