// A block that returns handler.Wait, or whose Hetzner Cloud call is held, answers waiting with its detail rather than failed, unless a failure is joined to the wait, and names the wait's end as lookAgainAt only when the wait knows it, and the earliest end of joined waits only when each knows its own.
package handler_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud/cloudtest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/calllog"
	"github.com/mitstack-eu/block-kit/verify/kittest"
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
	return []contract.CreatedResource{{Name: "r", Type: "aws.vpc", Identity: map[string]string{"vpcId": "vpc-1"}}}, handler.Wait("%s", waitDetail)
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
	blocks := map[string]handler.Resource{"Ensure": waitingBlock{}, "EnsureResources, having made r": waitingReporter{}, fromSignalWait: signalWaitingBlock{}}
	for _, op := range []string{"subscribe", "upgrade", "unsubscribe"} {
		for from, block := range blocks {
			t.Run(op+" from "+from, func(t *testing.T) {
				obs := fixed(contract.Observation{State: contract.Present})
				x := newHarness(t, block, func(c *handler.Config) { c.Observers = map[string]handler.Observer{"r": obs} })

				status, resp := x.answer(map[string]any{"operation": op})

				if status != http.StatusOK || resp.StatusDetail != waitDetail || resp.Error != "" {
					t.Errorf("%s answered %d with statusDetail %q and error %q, want 200 with %q", op, status, resp.StatusDetail, resp.Error, waitDetail)
				}
				if op == "unsubscribe" && (resp.Absent == nil || *resp.Absent) || op != "unsubscribe" && !resp.Pending {
					t.Errorf("%s answered pending %v and absent %v, want absent:false on an unsubscribe and pending:true otherwise", op, resp.Pending, resp.Absent)
				}
				if want := from == fromSignalWait && op != "unsubscribe"; resp.Redrive != want {
					t.Errorf("%s from %s answered redrive %v, want %v", op, from, resp.Redrive, want)
				}
				if calls := len(obs.calls()); calls != 0 {
					t.Errorf("the resource was observed %d times, want none once the block waits", calls)
				}
				if _, reports := block.(handler.ResourceReporter); reports && op != "unsubscribe" && (resp.Resources["r"].Ensure == nil || resp.Resources["r"].Ensure.Identity["vpcId"] != "vpc-1") {
					t.Errorf("%s answered resource r's ensure %+v, want the identity the ensure reported before it waited", op, resp.Resources["r"].Ensure)
				}
			})
		}
	}
}

const joinedFailure = "deleting primary IP 203.0.113.7: Hetzner Cloud answered 422"

var joined = errors.Join(timedWait(0), errors.New(joinedFailure))

// timedWait is a wait that names its end, heldRead's end plus after.
func timedWait(after time.Duration) error {
	return &handler.Waiting{Detail: waitDetail, LookAgainAt: heldRead.Until.Add(after)}
}

type joinedWaitBlock struct{}

func (joinedWaitBlock) Ensure(context.Context, contract.Desired) error { return joined }

func (joinedWaitBlock) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, fmt.Errorf("deleting: %w", joined)
}

func TestServe_AWaitJoinedWithAFailureAnswersTheFailure(t *testing.T) {
	for _, op := range []string{"subscribe", "unsubscribe"} {
		t.Run(op, func(t *testing.T) {
			obs := fixed(contract.Observation{State: contract.Present})
			x := newHarness(t, joinedWaitBlock{}, func(c *handler.Config) { c.Observers = map[string]handler.Observer{"r": obs} })

			status, resp := x.answer(map[string]any{"operation": op})

			if status != http.StatusInternalServerError || resp.Pending || resp.Absent != nil || !strings.Contains(resp.Error, joinedFailure) || resp.LookAgainAt != nil {
				t.Errorf("%s answered %d, pending %v, absent %v, error %q, lookAgainAt %v; want 500 with an error naming %q and no lookAgainAt", op, status, resp.Pending, resp.Absent, resp.Error, resp.LookAgainAt, joinedFailure)
			}
		})
	}
}

var heldRead = &cloud.RateLimited{Until: time.Date(2026, time.November, 19, 20, 1, 0, 0, time.UTC)}

func TestServe_AHeldReadAnswersPendingWithTheSecretOutputsAlreadyObservedPresent(t *testing.T) {
	deployed := map[string]string{"roleArn": "arn:aws:iam::1:role/deploy"}
	for _, op := range []string{"subscribe", "upgrade", "validate"} {
		t.Run(op, func(t *testing.T) {
			obs := roleObservers(map[string]string{"deploy-role": deployed["roleArn"], "audit-role": "arn:aws:iam::1:role/audit"})
			obs["ecr-push-role"] = &observer{err: fmt.Errorf("reading the role: %w", heldRead)}

			status, resp := serveRoleBlock(t, context.Background(), op, obs)

			if status != http.StatusOK || !resp.Pending || resp.StatusDetail != heldRead.Error() || !maps.Equal(resp.SecretOutputs, deployed) || len(resp.ResourceSecretOutputs) != 1 || !maps.Equal(resp.ResourceSecretOutputs["deploy-role"], deployed) {
				t.Errorf("%s answered %d, pending %v, statusDetail %q, secretOutputs %v, resourceSecretOutputs %v; want 200, pending, %q, and deploy-role's %v alone, since it was observed present before the read of ecr-push-role was held", op, status, resp.Pending, resp.StatusDetail, resp.SecretOutputs, resp.ResourceSecretOutputs, heldRead.Error(), deployed)
			}
		})
	}
}

func TestServe_AnUnsubscribeWaitingOnARemovalAnswersEachResourcesObserveAndKeepsWaitingWhenOneFails(t *testing.T) {
	const waitsOn = "removal pull request #1 of acme/weather, whose required check \"managed-ci\" is running"
	for _, tc := range []struct {
		name string
		obs  *observer
		want func(*contract.CallOutcome) bool
	}{
		{"made unsubscribing, with the removal it reports", fixed(contract.Observation{State: contract.Present, Removing: &contract.Removing{WaitsOn: waitsOn}}), func(o *contract.CallOutcome) bool {
			return o.State == contract.Present && o.Removing != nil && o.Removing.WaitsOn == waitsOn
		}},
		{"with the failed read's error and no removing", &observer{err: fmt.Errorf("github answered 502")}, func(o *contract.CallOutcome) bool { return o.Error != "" && o.Removing == nil }},
		{"unknown, with the held read's detail and no error", &observer{err: heldRead}, func(o *contract.CallOutcome) bool {
			return o.State == contract.Unknown && o.StatusDetail == heldRead.Error() && o.Error == "" && o.Type == "aws.vpc"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newHarness(t, &kittest.FakeResource{DeleteErr: handler.WaitOnRemoval("%s", waitDetail)}, func(c *handler.Config) { c.Observers = map[string]handler.Observer{"r": tc.obs} })

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

type cloudBlock func(ctx context.Context, step string) error

func (b cloudBlock) Ensure(ctx context.Context, _ contract.Desired) error { return b(ctx, "block") }

func (b cloudBlock) Delete(ctx context.Context, _ contract.Desired) (map[string]string, error) {
	return nil, b(ctx, "block")
}

func (b cloudBlock) PlanReplacements(ctx context.Context, _ contract.Desired) ([]string, error) {
	return nil, b(ctx, "plan")
}

func (b cloudBlock) Converging(ctx context.Context, _ contract.Desired) (string, bool, error) {
	return "", false, b(ctx, "converge")
}

func (b cloudBlock) ObserveResource(ctx context.Context, _ contract.ResourceRef) (contract.Observation, error) {
	return contract.Observation{State: contract.Present}, b(ctx, "observe")
}

// carries reports whether an answer's lookAgainAt is want in UTC, or left out when want is nil.
func carries(at, want *time.Time) bool {
	return at == nil && want == nil || at != nil && want != nil && at.Equal(*want) && at.Location() == time.UTC
}

// heldOn sends op twice to a block whose Hetzner Cloud call on step meets a 429, and checks that each answer waits until the hold's end, named in UTC, and that the hold answers the second without sending it.
func heldOn(t *testing.T, op, step string) {
	t.Helper()
	const endsUTC, token = "2026-11-19T20:01:00Z", "held-cloud-token"
	at, held := heldRead.Until.Add(-time.Minute), heldRead.Error()
	f, sent := cloudtest.New(t, token), &atomic.Int64{}
	f.Hook = func(w http.ResponseWriter, _ *http.Request) bool {
		sent.Add(1)
		w.Header().Set("RateLimit-Reset", fmt.Sprint(at.Add(30*time.Minute).Unix()))
		cloudtest.Refuse(w, http.StatusTooManyRequests, "rate_limit_exceeded", "Limit of '5000' requests per hour for ip '192.0.2.10' reached.")
		return true
	}
	client := cloud.NewHolds(func() time.Time { return at }).Client(f.URL(), token)
	b := cloudBlock(func(ctx context.Context, called string) error {
		if called != step {
			return nil
		}
		return client.KeepPrimaryIPs(ctx, map[string]string{"block": "test-block", "subscription": "sub-1"})
	})
	logs, x := captureLog(t), newHarness(t, b, func(cfg *handler.Config) { cfg.Observers = map[string]handler.Observer{"r": b} })

	for i := range 2 {
		status, resp := x.answer(map[string]any{"operation": op})

		if status != http.StatusOK || resp.StatusDetail != held || !strings.Contains(resp.StatusDetail, endsUTC) || resp.Error != "" || !carries(resp.LookAgainAt, &heldRead.Until) {
			t.Errorf("%s %d answered %d with statusDetail %q, error %q and lookAgainAt %v; want 200 with %q and lookAgainAt %s, the hold's end in UTC", op, i+1, status, resp.StatusDetail, resp.Error, resp.LookAgainAt, held, endsUTC)
		}
		if op == "unsubscribe" && (resp.Absent == nil || *resp.Absent) || op != "unsubscribe" && (!resp.Pending || resp.Absent != nil) {
			t.Errorf("%s %d answered pending %v and absent %v; want absent:false on an unsubscribe, else pending:true and no absent", op, i+1, resp.Pending, resp.Absent)
		}
		if o := resp.Resources["r"].Observe; step != "block" && (o == nil || o.State != contract.Unknown || o.StatusDetail != held || o.Error != "") {
			t.Errorf("%s %d answered resource r's observe %+v; want state unknown with statusDetail %q and no error", op, i+1, o, held)
		}
	}
	if want := "calls: subscription=sub-1 verb=" + op + " " + calllog.Host(strings.TrimSuffix(strings.TrimPrefix(f.URL(), "http://"), "/v1")) + "=1\n"; sent.Load() != 1 || strings.Count(logs.String(), "calls: ") != 1 || !strings.Contains(logs.String(), want) {
		t.Errorf("%d calls reached Hetzner over two %ss and logs = %q; want 1, the second answered by the hold, and the one call count line %q for it", sent.Load(), op, logs, want)
	}
}

type lookAgainCase struct {
	name, op string
	r        handler.Resource
	waits    bool
	at       *time.Time
}

func (tc lookAgainCase) check(t *testing.T) {
	status, resp := newHarness(t, tc.r).answer(map[string]any{"operation": tc.op})

	if waited := tc.op == "unsubscribe" && resp.Absent != nil && !*resp.Absent || tc.op != "unsubscribe" && resp.Pending; status != http.StatusOK || resp.Error != "" || waited != tc.waits {
		t.Errorf("%s answered %d, error %q, pending %v and absent %v; want 200, no error, and waiting %v, as absent:false on an unsubscribe and pending:true otherwise", tc.op, status, resp.Error, resp.Pending, resp.Absent, tc.waits)
	}
	if !carries(resp.LookAgainAt, tc.at) {
		t.Errorf("%s answered lookAgainAt %v, want %v in UTC", tc.op, resp.LookAgainAt, tc.at)
	}
}

func TestHandler_HeldStepNamesLookAgainAt(t *testing.T) {
	for _, op := range []string{"subscribe", "upgrade", "unsubscribe"} {
		t.Run(op+" whose ensure or delete is held", func(t *testing.T) { heldOn(t, op, "block") })
		t.Run(op+" whose observation is held", func(t *testing.T) { heldOn(t, op, "observe") })
	}
	for _, tc := range []lookAgainCase{
		{"a subscribe whose block waits until a time it names in another zone", "subscribe", &kittest.FakeResource{EnsureErr: &handler.Waiting{Detail: waitDetail, LookAgainAt: heldRead.Until.In(time.FixedZone("CEST", 2*60*60))}}, true, &heldRead.Until},
		{"an unsubscribe whose block waits on a removal until a time it names", "unsubscribe", &kittest.FakeResource{DeleteErr: &handler.Waiting{Detail: waitDetail, Removal: true, LookAgainAt: heldRead.Until}}, true, &heldRead.Until},
		{"a subscribe whose block joins two waits names the earlier end when it comes first", "subscribe", &kittest.FakeResource{EnsureErr: errors.Join(timedWait(0), timedWait(time.Hour))}, true, &heldRead.Until},
		{"a subscribe whose block joins two waits names the earlier end when it comes second", "subscribe", &kittest.FakeResource{EnsureErr: errors.Join(timedWait(time.Hour), timedWait(0))}, true, &heldRead.Until},
	} {
		t.Run(tc.name, tc.check)
	}
}

func TestHandler_HeldValidateNamesLookAgainAt(t *testing.T) {
	for _, step := range []string{"observe", "plan", "converge"} {
		t.Run("a validate whose "+step+" call is held answers the resource unknown", func(t *testing.T) { heldOn(t, "validate", step) })
	}
}

func TestHandler_AnswerWithoutAHoldOmitsLookAgainAt(t *testing.T) {
	for _, tc := range []lookAgainCase{
		{"a subscribe waiting on something with no known end answers pending", "subscribe", &kittest.FakeResource{EnsureErr: handler.Wait("%s", waitDetail)}, true, nil},
		{"an unsubscribe waiting on something with no known end answers absent false", "unsubscribe", &kittest.FakeResource{DeleteErr: handler.Wait("%s", waitDetail)}, true, nil},
		{"an unsubscribe waiting on a removal with no known end, whose observation is held, answers absent false, since the removal decides when it can change", "unsubscribe", &kittest.FakeResource{DeleteErr: handler.WaitOnRemoval("%s", waitDetail), ObserveErr: heldRead}, true, nil},
		{"a subscribe whose block joins a wait with no known end before a timed one answers pending", "subscribe", &kittest.FakeResource{EnsureErr: errors.Join(handler.Wait("%s", waitDetail), timedWait(0))}, true, nil},
		{"a subscribe whose block joins a timed wait before one with no known end answers pending", "subscribe", &kittest.FakeResource{EnsureErr: errors.Join(timedWait(0), handler.Wait("%s", waitDetail))}, true, nil},
		{"a subscribe that finishes answers", "subscribe", &kittest.FakeResource{}, false, nil},
		{"a validate of a present resource answers", "validate", &kittest.FakeResource{Count: 1}, false, nil},
	} {
		t.Run(tc.name+" with no lookAgainAt", tc.check)
	}
}
