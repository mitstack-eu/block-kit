// A block that returns handler.Wait, or whose Hetzner Cloud call is held, answers waiting with its detail rather than failed, unless a failure is joined to the wait.
package handler_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud/cloudtest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/calllog"
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

type waitingMaker struct{ waitingReporter }

func (waitingMaker) EnsureResources(context.Context, contract.Desired) ([]contract.CreatedResource, error) {
	return []contract.CreatedResource{{Name: "r", Type: "aws.vpc", Identity: map[string]string{"vpcId": "vpc-1"}}}, handler.Wait("%s", waitDetail)
}

const fromMaker = "EnsureResources, having made r"

type signalWaitingBlock struct{ waitingBlock }

func (signalWaitingBlock) Ensure(context.Context, contract.Desired) error {
	return fmt.Errorf("reading the host's report: %w", handler.WaitForSignal("%s", waitDetail))
}

func (signalWaitingBlock) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, handler.WaitForSignal("%s", waitDetail)
}

const fromSignalWait = "Ensure, waiting for a signal"

func TestServe_AWaitFromTheBlockAnswersPendingWithItsDetailAndOnlyAWaitForASignalAsksForTheSubscribeAgain(t *testing.T) {
	blocks := map[string]handler.Resource{"Ensure": waitingBlock{}, "EnsureResources": waitingReporter{}, fromMaker: waitingMaker{}, fromSignalWait: signalWaitingBlock{}}
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
				if e := resp.Resources["r"].Ensure; from == fromMaker && op != "unsubscribe" && (e == nil || e.Identity["vpcId"] != "vpc-1") {
					t.Errorf("%s answered resource r's ensure %+v, want the identity the ensure reported before it waited", op, e)
				}
			})
		}
	}
}

const joinedFailure = "deleting primary IP 203.0.113.7: Hetzner Cloud answered 422"

type joinedWaitBlock struct{}

func (joinedWaitBlock) Ensure(context.Context, contract.Desired) error {
	return errors.Join(handler.Wait("%s", waitDetail), errors.New(joinedFailure))
}

func (joinedWaitBlock) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, fmt.Errorf("deleting: %w", errors.Join(handler.Wait("%s", waitDetail), errors.New(joinedFailure)))
}

func TestServe_AWaitJoinedWithAFailureAnswersTheFailure(t *testing.T) {
	for _, op := range []string{"subscribe", "unsubscribe"} {
		t.Run(op, func(t *testing.T) {
			obs := fixed(contract.Observation{State: contract.Present})
			x := newHarness(t, joinedWaitBlock{}, func(c *handler.Config) { c.Observers = map[string]handler.Observer{"r": obs} })

			status, resp := x.answer(map[string]any{"operation": op})

			if status != http.StatusInternalServerError || resp.Pending || resp.Absent != nil || !strings.Contains(resp.Error, joinedFailure) {
				t.Errorf("%s answered %d, pending %v, absent %v, error %q; want 500 with an error naming %q", op, status, resp.Pending, resp.Absent, resp.Error, joinedFailure)
			}
		})
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

var heldAt = time.Date(2026, time.November, 19, 20, 0, 0, 0, time.UTC)

const heldToken = "held-cloud-token"

func refusedCloud(t *testing.T) (*cloud.Client, *atomic.Int64, string) {
	t.Helper()
	f := cloudtest.New(t, heldToken)
	sent := &atomic.Int64{}
	f.Hook = func(w http.ResponseWriter, _ *http.Request) bool {
		sent.Add(1)
		w.Header().Set("RateLimit-Reset", strconv.FormatInt(heldAt.Add(30*time.Minute).Unix(), 10))
		cloudtest.Refuse(w, http.StatusTooManyRequests, "rate_limit_exceeded", "Limit of '5000' requests per hour for ip '192.0.2.10' reached.")
		return true
	}
	u, err := url.Parse(f.URL())
	if err != nil {
		t.Fatalf("parsing the fake's URL %q: %v", f.URL(), err)
	}
	return cloud.NewHolds(func() time.Time { return heldAt }).Client(f.URL(), heldToken), sent, calllog.Host(u.Host)
}

var heldLabels = map[string]string{"block": "test-block", "subscription": "sub-1"}

type cloudBlock struct {
	c     *cloud.Client
	calls string
}

func (b cloudBlock) keep(ctx context.Context, step string) error {
	if b.calls != step {
		return nil
	}
	return b.c.KeepPrimaryIPs(ctx, heldLabels)
}

func (b cloudBlock) Ensure(ctx context.Context, _ contract.Desired) error {
	return b.keep(ctx, "block")
}

func (b cloudBlock) Delete(ctx context.Context, _ contract.Desired) (map[string]string, error) {
	if b.calls != "block" {
		return nil, nil
	}
	return nil, b.c.DeletePrimaryIPs(ctx, heldLabels)
}

func (b cloudBlock) PlanReplacements(ctx context.Context, _ contract.Desired) ([]string, error) {
	return nil, b.keep(ctx, "plan")
}

func (b cloudBlock) Converging(ctx context.Context, _ contract.Desired) (string, bool, error) {
	return "", false, b.keep(ctx, "converge")
}

func (b cloudBlock) ObserveResource(ctx context.Context, _ contract.ResourceRef) (contract.Observation, error) {
	if err := b.keep(ctx, "observe"); err != nil {
		return contract.Observation{}, err
	}
	return contract.Observation{State: contract.Present}, nil
}

func TestServe_AHeldHetznerCallAnswersAStepWaitingAndAValidateUnknownNamingInUTCWhenTheHoldEnds(t *testing.T) {
	const endsUTC = "2026-11-19T20:01:00Z"
	held := (&cloud.RateLimited{Until: heldAt.Add(time.Minute)}).Error()
	cases := []struct{ name, op, calls string }{
		{"a subscribe whose ensure is held answers waiting", "subscribe", "block"},
		{"an upgrade whose ensure is held answers waiting", "upgrade", "block"},
		{"an unsubscribe whose delete is held answers not yet absent", "unsubscribe", "block"},
		{"a subscribe whose observation is held answers waiting with the resource unknown", "subscribe", "observe"},
		{"an upgrade whose observation is held answers waiting with the resource unknown", "upgrade", "observe"},
		{"an unsubscribe whose observation is held answers not yet absent with the resource unknown", "unsubscribe", "observe"},
		{"a validate whose observation is held answers the resource unknown", "validate", "observe"},
		{"a validate whose replacement plan is held answers the resource unknown", "validate", "plan"},
		{"a validate whose convergence read is held answers the resource unknown", "validate", "converge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, sent, host := refusedCloud(t)
			logs := captureLog(t)
			b := cloudBlock{c, tc.calls}
			x := newHarness(t, b, func(cfg *handler.Config) { cfg.Observers = map[string]handler.Observer{"r": b} })

			for i := range 2 {
				status, resp := x.answer(map[string]any{"operation": tc.op})

				if status != http.StatusOK || resp.StatusDetail != held || !strings.Contains(resp.StatusDetail, endsUTC) || resp.Error != "" {
					t.Errorf("%s %d answered %d with statusDetail %q and error %q; want 200 with %q, naming %s", tc.op, i+1, status, resp.StatusDetail, resp.Error, held, endsUTC)
				}
				if tc.op == "unsubscribe" && (resp.Absent == nil || *resp.Absent) {
					t.Errorf("unsubscribe %d answered absent %v; want absent:false", i+1, resp.Absent)
				}
				if tc.op != "unsubscribe" && (!resp.Pending || resp.Absent != nil) {
					t.Errorf("%s %d answered pending %v and absent %v; want pending:true and no absent", tc.op, i+1, resp.Pending, resp.Absent)
				}
				if o := resp.Resources["r"].Observe; tc.calls != "block" && (o == nil || o.State != contract.Unknown || o.StatusDetail != held || o.Error != "") {
					t.Errorf("%s %d answered resource r's observe %+v; want state unknown with statusDetail %q and no error", tc.op, i+1, o, held)
				}
			}
			if got := sent.Load(); got != 1 {
				t.Errorf("%d calls reached Hetzner over two %ss; want 1, the second answered by the hold", got, tc.op)
			}
			want := "calls: subscription=sub-1 verb=" + tc.op + " " + host + "=1\n"
			if strings.Count(logs.String(), "calls: ") != 1 || !strings.Contains(logs.String(), want) {
				t.Errorf("logs = %q; want the one call count line %q, for the request that reached Hetzner and none for the held one", logs, want)
			}
		})
	}
}
