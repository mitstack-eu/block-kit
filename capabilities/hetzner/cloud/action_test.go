package cloud_test

// The action waiter returns once every action finishes, and otherwise names what the actions do beside Hetzner's error or the caller's context's error.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud"
)

const (
	statusRunning = "running"
	statusSuccess = "success"
	statusError   = "error"

	serverCreate = "server web-1 to be created"
)

// action is a Hetzner Cloud action as the block's own client reports it.
type action struct {
	id            int64
	status        string
	code, message string
}

// actionError is worded as hcloud-go's ActionError, which names Hetzner's message, code and action id.
type actionError struct {
	id            int64
	code, message string
}

func (e actionError) Error() string { return fmt.Sprintf("%s (%s, %d)", e.message, e.code, e.id) }

// clientWaiter stands in for the block client's waiter the kit's waiter takes, as hcloud-go's ActionClient.WaitFor answers, and records each id it waited on.
type clientWaiter struct {
	t      *testing.T
	waited []int64
}

func (c *clientWaiter) WaitFor(ctx context.Context, actions ...*action) error {
	for _, a := range actions {
		c.waited = append(c.waited, a.id)
	}
	for _, a := range actions {
		switch a.status {
		case statusError:
			return actionError{id: a.id, code: a.code, message: a.message}
		case statusRunning:
			if ctx.Err() == nil {
				c.t.Errorf("action %d still runs and the client's waiter got a context that has not ended, so the caller's bound never reached it", a.id)
				return errors.New("the caller's context did not reach the client's waiter")
			}
			return fmt.Errorf("%w: remaining running actions: [%d]", ctx.Err(), a.id)
		}
	}
	return nil
}

func TestWaitForActions_ReturnsOnceEveryActionOfAServerCreateFinishes(t *testing.T) {
	client := &clientWaiter{t: t}
	create, next := &action{id: 41, status: statusSuccess}, &action{id: 42, status: statusSuccess}

	err := cloud.WaitForActions(ctx, client.WaitFor, serverCreate, create, next)

	if want := []int64{41, 42}; err != nil || !slices.Equal(client.waited, want) {
		t.Errorf("WaitForActions(%q, actions 41 and 42) = %v, waiting on %v; want nil, waiting on %v", serverCreate, err, client.waited, want)
	}
}

func TestWaitForActions_FailsNamingTheActionAndHetznersErrorWhenHetznerReportsOne(t *testing.T) {
	client := &clientWaiter{t: t}
	failed := &action{id: 42, status: statusError, code: "server_limit_exceeded", message: "server limit exceeded"}

	err := cloud.WaitForActions(ctx, client.WaitFor, serverCreate, failed)

	want := "waiting for server web-1 to be created: server limit exceeded (server_limit_exceeded, 42)"
	var hetzner actionError
	if err == nil || err.Error() != want || !errors.As(err, &hetzner) || hetzner.code != failed.code {
		t.Errorf("WaitForActions(%q, failed action 42) = %v; want %q, wrapping Hetzner's error with its code %q", serverCreate, err, want, failed.code)
	}
}

func TestWaitForActions_ReturnsTheContextsErrorWhenAnActionOutlastsTheVerb(t *testing.T) {
	client := &clientWaiter{t: t}
	running := &action{id: 42, status: statusRunning}
	verb, cancel := context.WithTimeout(ctx, 0)
	t.Cleanup(cancel)

	err := cloud.WaitForActions(verb, client.WaitFor, serverCreate, running)

	want := "waiting for server web-1 to be created: context deadline exceeded: remaining running actions: [42]"
	if err == nil || err.Error() != want || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitForActions(ended context, %q, running action 42) = %v; want %q, wrapping the context's error", serverCreate, err, want)
	}
}
