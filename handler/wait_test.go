// A block that returns handler.Wait, or whose Hetzner Cloud call is held, answers pending with its detail rather than failed.
package handler_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
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

var heldAt = time.Date(2026, time.November, 19, 20, 0, 0, 0, time.UTC)

const heldToken = "held-cloud-token"

// refusedCloud is a Hetzner Cloud fake refusing every call for the rate limit, returned with a client whose holds read a clock standing at heldAt, the count of calls reaching the fake, and the fake's host as the call log names it.
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

type quietBlock struct{}

func (quietBlock) Ensure(context.Context, contract.Desired) error { return nil }

func (quietBlock) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, nil
}

type cloudCallingBlock struct{ c *cloud.Client }

func (b cloudCallingBlock) Ensure(ctx context.Context, _ contract.Desired) error {
	return b.c.KeepPrimaryIPs(ctx, heldLabels)
}

func (b cloudCallingBlock) Delete(ctx context.Context, _ contract.Desired) (map[string]string, error) {
	return nil, b.c.DeletePrimaryIPs(ctx, heldLabels)
}

type cloudPlanner struct {
	quietBlock
	c *cloud.Client
}

func (b cloudPlanner) PlanReplacements(ctx context.Context, _ contract.Desired) ([]string, error) {
	return nil, b.c.KeepPrimaryIPs(ctx, heldLabels)
}

type cloudConverger struct {
	quietBlock
	c *cloud.Client
}

func (b cloudConverger) Converging(ctx context.Context, _ contract.Desired) (string, bool, error) {
	return "", false, b.c.KeepPrimaryIPs(ctx, heldLabels)
}

type cloudObserver struct{ c *cloud.Client }

func (o cloudObserver) ObserveResource(ctx context.Context, _ contract.ResourceRef) (contract.Observation, error) {
	if err := o.c.KeepPrimaryIPs(ctx, heldLabels); err != nil {
		return contract.Observation{}, err
	}
	return contract.Observation{State: contract.Present}, nil
}

// countLines are the per-request call count lines in logs, each as its fields.
func countLines(logs *bytes.Buffer) (out []map[string]string) {
	for _, l := range strings.Split(logs.String(), "\n") {
		if _, rest, ok := strings.Cut(l, "calls: "); ok {
			fields := map[string]string{}
			for _, kv := range strings.Fields(rest) {
				k, v, _ := strings.Cut(kv, "=")
				fields[k] = v
			}
			out = append(out, fields)
		}
	}
	return out
}

type answered struct {
	status int
	resp   contract.Response
}

func TestServe_AHeldHetznerCallAnswersAStepWaitingAndAValidateUnknownNamingInUTCWhenTheHoldEnds(t *testing.T) {
	const endsUTC = "2026-11-19T20:01:00Z"
	held := (&cloud.RateLimited{Until: heldAt.Add(time.Minute)}).Error()
	callingBlock := func(c *cloud.Client) handler.Resource { return cloudCallingBlock{c} }
	quiet := func(*cloud.Client) handler.Resource { return quietBlock{} }
	present := func(*cloud.Client) handler.Observer { return fixed(contract.Observation{State: contract.Present}) }
	reading := func(c *cloud.Client) handler.Observer { return cloudObserver{c} }
	cases := []struct {
		name, op string
		block    func(*cloud.Client) handler.Resource
		observer func(*cloud.Client) handler.Observer
		// unknown is set when the answer carries resource r, which then reads unknown.
		unknown bool
	}{
		{"a subscribe whose ensure is held answers waiting", "subscribe", callingBlock, present, false},
		{"an upgrade whose ensure is held answers waiting", "upgrade", callingBlock, present, false},
		{"an unsubscribe whose delete is held answers not yet absent", "unsubscribe", callingBlock, present, false},
		{"a subscribe whose observation is held answers waiting with the resource unknown", "subscribe", quiet, reading, true},
		{"an upgrade whose observation is held answers waiting with the resource unknown", "upgrade", quiet, reading, true},
		{"an unsubscribe whose observation is held answers not yet absent with the resource unknown", "unsubscribe", quiet, reading, true},
		{"a validate whose observation is held answers the resource unknown", "validate", quiet, reading, true},
		{"a validate whose replacement plan is held answers the resource unknown", "validate", func(c *cloud.Client) handler.Resource { return cloudPlanner{c: c} }, present, true},
		{"a validate whose convergence read is held answers the resource unknown", "validate", func(c *cloud.Client) handler.Resource { return cloudConverger{c: c} }, present, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, sent, host := refusedCloud(t)
			logs := captureLog(t)
			x := newHarness(t, tc.block(c), func(cfg *handler.Config) { cfg.Observers = map[string]handler.Observer{"r": tc.observer(c)} })

			var answers []answered
			for range 2 {
				status, resp := x.answer(map[string]any{"operation": tc.op})
				answers = append(answers, answered{status, resp})
			}

			for i, a := range answers {
				if a.status != http.StatusOK || a.resp.StatusDetail != held || !strings.Contains(a.resp.StatusDetail, endsUTC) || a.resp.Error != "" {
					t.Errorf("%s %d answered %d with statusDetail %q and error %q; want 200 with %q, naming %s", tc.op, i+1, a.status, a.resp.StatusDetail, a.resp.Error, held, endsUTC)
				}
				if tc.op == "unsubscribe" && (a.resp.Absent == nil || *a.resp.Absent) {
					t.Errorf("unsubscribe %d answered absent %v; want absent:false", i+1, a.resp.Absent)
				}
				if tc.op != "unsubscribe" && (!a.resp.Pending || a.resp.Absent != nil) {
					t.Errorf("%s %d answered pending %v and absent %v; want pending:true and no absent, so nothing reads missing", tc.op, i+1, a.resp.Pending, a.resp.Absent)
				}
				if o := a.resp.Resources["r"].Observe; tc.unknown && (o == nil || o.State != contract.Unknown || o.StatusDetail != held || o.Error != "") {
					t.Errorf("%s %d answered resource r's observe %+v; want state unknown with statusDetail %q and no error", tc.op, i+1, o, held)
				}
			}
			if got := sent.Load(); got != 1 {
				t.Errorf("%d calls reached Hetzner over two %ss; want 1, the second answered by the hold", got, tc.op)
			}
			want := []map[string]string{{"subscription": "sub-1", "verb": tc.op, host: "1"}}
			if got := countLines(logs); !reflect.DeepEqual(got, want) {
				t.Errorf("call count lines = %v in logs %q; want %v, one for the request that reached Hetzner and none for the held one", got, logs, want)
			}
		})
	}
}
