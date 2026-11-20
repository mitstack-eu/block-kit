// A block that returns handler.Wait, or whose Hetzner Cloud call is held, answers waiting with its detail rather than failed, unless a failure is joined to the wait.
package handler_test

import (
	"context"
	"encoding/json"
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

var joined = errors.Join(handler.Wait("%s", waitDetail), errors.New(joinedFailure))

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

			if status != http.StatusInternalServerError || resp.Pending || resp.Absent != nil || !strings.Contains(resp.Error, joinedFailure) {
				t.Errorf("%s answered %d, pending %v, absent %v, error %q; want 500 with an error naming %q", op, status, resp.Pending, resp.Absent, resp.Error, joinedFailure)
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
		{"made unsubscribing, with the removal it reports", fixed(contract.Observation{State: contract.Present, Removing: &contract.Removing{WaitsOn: waitsOn}}), func(o *contract.CallOutcome) bool {
			return o.State == contract.Present && o.Removing != nil && o.Removing.WaitsOn == waitsOn
		}},
		{"with the failed read's error and no removing", &observer{err: fmt.Errorf("github answered 502")}, func(o *contract.CallOutcome) bool { return o.Error != "" && o.Removing == nil }},
		{"unknown, with the held read's detail and no error", &observer{err: heldRead}, func(o *contract.CallOutcome) bool {
			return o.State == contract.Unknown && o.StatusDetail == heldRead.Error() && o.Error == "" && o.Type == "aws.vpc"
		}},
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

// heldEndsUTC is heldRead.Until as the wire carries it.
const heldEndsUTC = "2026-11-19T20:01:00Z"

// heldCloud is a block whose Hetzner Cloud call on step meets a 429 and is held until heldRead.Until, with the fake and the count of calls that reached it.
func heldCloud(t *testing.T, step string) (cloudBlock, *cloudtest.Fake, *atomic.Int64) {
	t.Helper()
	const token = "held-cloud-token"
	at := heldRead.Until.Add(-time.Minute)
	f, sent := cloudtest.New(t, token), &atomic.Int64{}
	f.Hook = func(w http.ResponseWriter, _ *http.Request) bool {
		sent.Add(1)
		w.Header().Set("RateLimit-Reset", fmt.Sprint(at.Add(30*time.Minute).Unix()))
		cloudtest.Refuse(w, http.StatusTooManyRequests, "rate_limit_exceeded", "Limit of '5000' requests per hour for ip '192.0.2.10' reached.")
		return true
	}
	client := cloud.NewHolds(func() time.Time { return at }).Client(f.URL(), token)
	return cloudBlock(func(ctx context.Context, called string) error {
		if called != step {
			return nil
		}
		return client.KeepPrimaryIPs(ctx, map[string]string{"block": "test-block", "subscription": "sub-1"})
	}), f, sent
}

func TestServe_AHeldHetznerCallAnswersAStepWaitingAndAValidateUnknownNamingInUTCWhenTheHoldEnds(t *testing.T) {
	const endsUTC = heldEndsUTC
	held := heldRead.Error()
	for _, tc := range []struct{ name, op, calls string }{
		{"a subscribe whose ensure is held answers waiting", "subscribe", "block"},
		{"an upgrade whose ensure is held answers waiting", "upgrade", "block"},
		{"an unsubscribe whose delete is held answers not yet absent", "unsubscribe", "block"},
		{"a subscribe whose observation is held answers waiting with the resource unknown", "subscribe", "observe"},
		{"an upgrade whose observation is held answers waiting with the resource unknown", "upgrade", "observe"},
		{"an unsubscribe whose observation is held answers not yet absent with the resource unknown", "unsubscribe", "observe"},
		{"a validate whose observation is held answers the resource unknown", "validate", "observe"},
		{"a validate whose replacement plan is held answers the resource unknown", "validate", "plan"},
		{"a validate whose convergence read is held answers the resource unknown", "validate", "converge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, f, sent := heldCloud(t, tc.calls)
			logs, x := captureLog(t), newHarness(t, b, func(cfg *handler.Config) { cfg.Observers = map[string]handler.Observer{"r": b} })

			for i := range 2 {
				status, resp := x.answer(map[string]any{"operation": tc.op})

				if status != http.StatusOK || resp.StatusDetail != held || !strings.Contains(resp.StatusDetail, endsUTC) || resp.Error != "" {
					t.Errorf("%s %d answered %d with statusDetail %q and error %q; want 200 with %q, naming %s", tc.op, i+1, status, resp.StatusDetail, resp.Error, held, endsUTC)
				}
				if tc.op == "unsubscribe" && (resp.Absent == nil || *resp.Absent) || tc.op != "unsubscribe" && (!resp.Pending || resp.Absent != nil) {
					t.Errorf("%s %d answered pending %v and absent %v; want absent:false on an unsubscribe, else pending:true and no absent", tc.op, i+1, resp.Pending, resp.Absent)
				}
				if o := resp.Resources["r"].Observe; tc.calls != "block" && (o == nil || o.State != contract.Unknown || o.StatusDetail != held || o.Error != "") {
					t.Errorf("%s %d answered resource r's observe %+v; want state unknown with statusDetail %q and no error", tc.op, i+1, o, held)
				}
			}
			if want := "calls: subscription=sub-1 verb=" + tc.op + " " + calllog.Host(strings.TrimSuffix(strings.TrimPrefix(f.URL(), "http://"), "/v1")) + "=1\n"; sent.Load() != 1 || strings.Count(logs.String(), "calls: ") != 1 || !strings.Contains(logs.String(), want) {
				t.Errorf("%d calls reached Hetzner over two %ss and logs = %q; want 1, the second answered by the hold, and the one call count line %q for it", sent.Load(), tc.op, logs, want)
			}
		})
	}
}

// timedWaitBlock waits until a time it names itself, in whatever zone that time carries.
type timedWaitBlock struct{ until time.Time }

func (b timedWaitBlock) Ensure(context.Context, contract.Desired) error {
	return fmt.Errorf("reading the server: %w", &handler.Waiting{Detail: waitDetail, LookAgainAt: b.until})
}

func (b timedWaitBlock) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, &handler.Waiting{Detail: waitDetail, Removal: true, LookAgainAt: b.until}
}

type lookAgainCase struct {
	name   string
	op     string
	serve  func(t *testing.T) *harness
	fields map[string]any
}

func onCloud(step string) func(t *testing.T) *harness {
	return func(t *testing.T) *harness {
		t.Helper()
		b, _, _ := heldCloud(t, step)
		return newHarness(t, b, func(cfg *handler.Config) { cfg.Observers = map[string]handler.Observer{"r": b} })
	}
}

func onBlock(r handler.Resource, obs handler.Observer) func(t *testing.T) *harness {
	return func(t *testing.T) *harness {
		t.Helper()
		return newHarness(t, r, func(cfg *handler.Config) { cfg.Observers = map[string]handler.Observer{"r": obs} })
	}
}

func onSnapshot(err error) func(t *testing.T) *harness {
	return func(t *testing.T) *harness {
		t.Helper()
		block := newSnapshottingBlock("hnd_0001")
		block.err = err
		return newHarness(t, block, withManifest(snapshotManifest), func(cfg *handler.Config) { cfg.Observers = asObservers(blockObservers(newFakeCloud())) })
	}
}

// answerOf posts the case's operation and returns the status, the conforming answer, and the lookAgainAt its body carries on the wire, if any.
func answerOf(t *testing.T, tc lookAgainCase) (int, contract.Response, string, bool) {
	t.Helper()
	status, raw := tc.serve(t).post(map[string]any{"operation": tc.op}, tc.fields)
	assertConforms(t, raw)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("%s answered %s, which is not a JSON object: %v", tc.op, raw, err)
	}
	wire, carried := fields["lookAgainAt"]
	var at string
	if carried {
		if err := json.Unmarshal(wire, &at); err != nil {
			t.Fatalf("%s answered lookAgainAt %s, which is not a JSON string: %v", tc.op, wire, err)
		}
	}
	return status, decodeResponse(t, raw), at, carried
}

func TestHandler_HeldStepNamesLookAgainAt(t *testing.T) {
	inCEST := heldRead.Until.In(time.FixedZone("CEST", 2*60*60))
	present := fixed(contract.Observation{State: contract.Present})
	for _, tc := range []lookAgainCase{
		{"a subscribe whose ensure is held answers pending until the hold ends", "subscribe", onCloud("block"), nil},
		{"an upgrade whose ensure is held answers pending until the hold ends", "upgrade", onCloud("block"), nil},
		{"a subscribe whose observation is held answers pending until the hold ends", "subscribe", onCloud("observe"), nil},
		{"an upgrade whose observation is held answers pending until the hold ends", "upgrade", onCloud("observe"), nil},
		{"an unsubscribe whose delete is held answers absent false until the hold ends", "unsubscribe", onCloud("block"), nil},
		{"an unsubscribe whose observation is held answers absent false until the hold ends", "unsubscribe", onCloud("observe"), nil},
		{"a snapshot whose call is held answers pending until the hold ends", "snapshot", onSnapshot(fmt.Errorf("checking the scratch copy: %w", heldRead)), map[string]any{"unit": "app-data"}},
		{"a subscribe whose block waits until a time it names in another zone answers pending naming that time in UTC", "subscribe", onBlock(timedWaitBlock{until: inCEST}, present), nil},
		{"an unsubscribe whose block waits on a removal until a time it names in another zone answers absent false naming that time in UTC", "unsubscribe", onBlock(timedWaitBlock{until: inCEST}, present), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, resp, at, carried := answerOf(t, tc)

			if status != http.StatusOK || resp.Error != "" {
				t.Errorf("%s answered %d with error %q, want 200 and no error", tc.op, status, resp.Error)
			}
			if tc.op == "unsubscribe" && (resp.Absent == nil || *resp.Absent) || tc.op != "unsubscribe" && !resp.Pending {
				t.Errorf("%s answered pending %v and absent %v; want absent:false on an unsubscribe and pending:true otherwise", tc.op, resp.Pending, resp.Absent)
			}
			if !carried || at != heldEndsUTC {
				t.Errorf("%s answered lookAgainAt %q, carried %v; want %q, the hold's end in UTC", tc.op, at, carried, heldEndsUTC)
			}
		})
	}
}

func TestHandler_HeldValidateNamesLookAgainAt(t *testing.T) {
	for _, tc := range []lookAgainCase{
		{"a validate whose observation is held answers the resource unknown until the hold ends", "validate", onCloud("observe"), nil},
		{"a validate whose replacement plan is held answers the resource unknown until the hold ends", "validate", onCloud("plan"), nil},
		{"a validate whose convergence read is held answers the resource unknown until the hold ends", "validate", onCloud("converge"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, resp, at, carried := answerOf(t, tc)

			if status != http.StatusOK || !resp.Pending || resp.Error != "" {
				t.Errorf("validate answered %d, pending %v, error %q; want 200, pending and no error", status, resp.Pending, resp.Error)
			}
			if o := resp.Resources["r"].Observe; o == nil || o.State != contract.Unknown {
				t.Errorf("validate answered resource r's observe %+v, want state unknown", o)
			}
			if !carried || at != heldEndsUTC {
				t.Errorf("validate answered lookAgainAt %q, carried %v; want %q, the hold's end in UTC", at, carried, heldEndsUTC)
			}
		})
	}
}

func TestHandler_AnswerWithoutAHoldOmitsLookAgainAt(t *testing.T) {
	present := fixed(contract.Observation{State: contract.Present})
	finishes := cloudBlock(func(context.Context, string) error { return nil })
	for _, tc := range []struct {
		lookAgainCase
		waits bool
	}{
		{lookAgainCase{"a subscribe waiting on something with no known end answers pending with no lookAgainAt", "subscribe", onBlock(waitingBlock{}, present), nil}, true},
		{lookAgainCase{"an unsubscribe waiting on something with no known end answers absent false with no lookAgainAt", "unsubscribe", onBlock(waitingBlock{}, present), nil}, true},
		{lookAgainCase{"an unsubscribe waiting on a removal with no known end, whose observation is held, answers absent false with no lookAgainAt, since the removal decides when it ends", "unsubscribe", onBlock(removalWaitingBlock{}, &observer{err: heldRead}), nil}, true},
		{lookAgainCase{"a snapshot waiting on something with no known end answers pending with no lookAgainAt", "snapshot", onSnapshot(handler.Wait("the scratch copy of app-data is restoring")), map[string]any{"unit": "app-data"}}, true},
		{lookAgainCase{"a subscribe that finishes answers with no lookAgainAt", "subscribe", onBlock(finishes, finishes), nil}, false},
		{lookAgainCase{"a validate of a present resource answers with no lookAgainAt", "validate", onBlock(finishes, finishes), nil}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, resp, at, carried := answerOf(t, tc.lookAgainCase)

			if status != http.StatusOK || resp.Error != "" {
				t.Errorf("%s answered %d with error %q, want 200 and no error", tc.op, status, resp.Error)
			}
			if waited := resp.Pending || resp.Absent != nil && !*resp.Absent; waited != tc.waits {
				t.Errorf("%s answered pending %v and absent %v; want it waiting %v", tc.op, resp.Pending, resp.Absent, tc.waits)
			}
			if carried {
				t.Errorf("%s answered lookAgainAt %q, want the field left out", tc.op, at)
			}
		})
	}
}
