// A block that returns handler.Wait answers pending with its detail, not failed, and observes nothing.
package handler_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

const waitDetail = "the provider's hourly request limit is reached, so nothing was changed"

type waitingBlock struct{}

func (waitingBlock) Ensure(context.Context, contract.Desired) error {
	return fmt.Errorf("reading the server: %w", handler.Wait("%s", waitDetail))
}

func (waitingBlock) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, handler.Wait("%s", waitDetail)
}

// waitingReporter waits from EnsureResources, which the handler calls in place of Ensure.
type waitingReporter struct{ waitingBlock }

func (waitingReporter) Ensure(context.Context, contract.Desired) error { return nil }

func (waitingReporter) EnsureResources(context.Context, contract.Desired) ([]contract.CreatedResource, error) {
	return nil, handler.Wait("%s", waitDetail)
}

func TestServe_AWaitFromTheBlockAnswersPendingWithItsDetail(t *testing.T) {
	blocks := map[string]handler.Resource{"Ensure": waitingBlock{}, "EnsureResources": waitingReporter{}}
	for _, op := range []string{"subscribe", "upgrade", "unsubscribe"} {
		for from, block := range blocks {
			t.Run(op+" from "+from, func(t *testing.T) {
				obs := fixed(contract.Observation{State: contract.Present})
				x := newHarness(t, block, func(c *handler.Config) { c.Observers = map[string]handler.Observer{"r": obs} })

				status, resp := x.answer(map[string]any{"operation": op})

				if status != http.StatusOK || resp.StatusDetail != waitDetail || resp.Error != "" {
					t.Errorf("%s answered %d with statusDetail %q and error %q, want 200 with %q", op, status, resp.StatusDetail, resp.Error, waitDetail)
				}
				if op == "unsubscribe" {
					if resp.Absent == nil || *resp.Absent {
						t.Errorf("unsubscribe answered absent %v, want absent:false", resp.Absent)
					}
				} else if !resp.Pending {
					t.Errorf("%s answered settled, want pending:true", op)
				}
				if calls := len(obs.calls()); calls != 0 {
					t.Errorf("the resource was observed %d times, want none once the block waits", calls)
				}
			})
		}
	}
}
