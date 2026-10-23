// The settle loop stops when the caller has gone, since defaultSleep returns at once on a done context and a cancelled request would fire its whole remaining budget unpaced.
package handler_test

import (
	"context"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

func TestServe_SettleStopsEarlyWhenTheContextIsCancelled(t *testing.T) {
	const budget = 1000
	for op, never := range map[string]contract.State{"subscribe": contract.Absent, "unsubscribe": contract.Present} {
		t.Run(op, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			polled := fixed(contract.Observation{State: never})
			x := newHarness(t, &blockResource{cloud: newFakeCloud()}, withManifest(onlyAManifest), withBudget(budget), func(c *handler.Config) {
				c.Observers = map[string]handler.Observer{"a": cancellingObserver{Observer: polled, settledOn: never, cancel: cancel}}
			})
			req := x.request(map[string]any{"operation": op}).WithContext(ctx)
			done := make(chan struct{})

			go func() {
				x.serve(req)
				close(done)
			}()

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Serve did not return after its context was cancelled: it kept spending the settle budget")
			}
			if calls := len(polled.calls()); calls >= budget {
				t.Errorf("the resource was observed %d times, want strictly under the %d budget", calls, budget)
			}
		})
	}
}
