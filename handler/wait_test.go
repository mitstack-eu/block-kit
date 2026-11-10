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

type waitingReporter struct{ waitingBlock }

func (waitingReporter) Ensure(context.Context, contract.Desired) error { return nil }

func (waitingReporter) EnsureResources(context.Context, contract.Desired) ([]contract.CreatedResource, error) {
	return nil, handler.Wait("%s", waitDetail)
}

type signalWaitingBlock struct{ waitingBlock }

func (signalWaitingBlock) Ensure(context.Context, contract.Desired) error {
	return fmt.Errorf("reading the host's report: %w", handler.WaitForSignal("%s", waitDetail))
}

func (signalWaitingBlock) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, handler.WaitForSignal("%s", waitDetail)
}

const fromSignalWait = "Ensure, waiting for a signal"

func TestServe_AWaitFromTheBlockAnswersPendingWithItsDetailAndOnlyAWaitForASignalAsksForTheSubscribeAgain(t *testing.T) {
	blocks := map[string]handler.Resource{"Ensure": waitingBlock{}, "EnsureResources": waitingReporter{}, fromSignalWait: signalWaitingBlock{}}
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
				if want := from == fromSignalWait && op != "unsubscribe"; resp.Redrive != want {
					t.Errorf("%s from %s answered redrive %v, want %v", op, from, resp.Redrive, want)
				}
				if calls := len(obs.calls()); calls != 0 {
					t.Errorf("the resource was observed %d times, want none once the block waits", calls)
				}
			})
		}
	}
}

type removalWaitingBlock struct{ waitingBlock }

func (removalWaitingBlock) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, handler.WaitOnRemoval("%s", waitDetail)
}

func TestServe_AnUnsubscribeWaitingOnARemovalAnswersEachResourcesObserveAndKeepsWaitingWhenOneFails(t *testing.T) {
	const waitsOn = "removal pull request #1 of acme/weather, whose required check \"managed-ci\" is running"
	for _, tc := range []struct {
		name string
		obs  *observer
		want func(*contract.CallOutcome) bool
	}{
		{"made unsubscribing, with the removal it reports", fixed(contract.Observation{State: contract.Present, Removing: &contract.Removing{WaitsOn: waitsOn}}),
			func(o *contract.CallOutcome) bool { return o.State == contract.Present && o.Removing != nil && o.Removing.WaitsOn == waitsOn }},
		{"with the failed read's error and no removing", &observer{err: fmt.Errorf("github answered 502")},
			func(o *contract.CallOutcome) bool { return o.Error != "" && o.Removing == nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newHarness(t, removalWaitingBlock{}, func(c *handler.Config) { c.Observers = map[string]handler.Observer{"r": tc.obs} })

			status, resp := x.answer(map[string]any{"operation": "unsubscribe"})

			if status != http.StatusOK || resp.Absent == nil || *resp.Absent || resp.StatusDetail != waitDetail {
				t.Errorf("unsubscribe answered %d, absent %v, statusDetail %q; want 200, absent:false, %q", status, resp.Absent, resp.StatusDetail, waitDetail)
			}
			if o := resp.Resources["r"].Observe; o == nil || !tc.want(o) {
				t.Errorf("resource r's observe = %+v; want it %s", o, tc.name)
			}
			if refs := tc.obs.calls(); len(refs) != 1 || !refs[0].Unsubscribing {
				t.Errorf("observer calls = %+v, want one, made unsubscribing", refs)
			}
		})
	}
}
