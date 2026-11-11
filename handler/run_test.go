// A run asks a block on /hook to run one command, and the handler hands it to the block's run function only when the manifest lists run, answering what the function returns, fresh on every call, with the request's secrets scrubbed.
package handler_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

type runFunction struct {
	requests []contract.RunRequest
	desired  []contract.Desired
	result   contract.RunResult
	err      error
}

func (r *runFunction) run(_ context.Context, d contract.Desired, req contract.RunRequest) (contract.RunResult, error) {
	r.requests = append(r.requests, req)
	r.desired = append(r.desired, d)
	return r.result, r.err
}

func listsRun(t *testing.T) []byte {
	return withManifestEdit(t, plainV1Beta(t), "validate]", "validate, run]")
}

func servesRun(t *testing.T, r *runFunction) func(*handler.Config) {
	return then(servesBlock(listsRun(t)), func(c *handler.Config) { c.Run = r.run })
}

func runFields(extra map[string]any) map[string]any {
	fields := map[string]any{
		"operation": "run", "blockId": "plain", "appId": "app-1", "idempotencyKey": "sub-1:run:run_4c1e9a7f2b3d",
		"params": map[string]any{"appName": "shop"},
		"runId":  "run_4c1e9a7f2b3d", "command": "php bin/add-user.php alice@example.com", "process": "web",
	}
	for k, v := range extra {
		fields[k] = v
	}
	return fields
}

func TestNew_RefusesARunDeclarationWithoutItsFunction(t *testing.T) {
	naming := map[string]func(*handler.Config){
		"Config.Run":        servesBlock(listsRun(t)),
		"does not list run": then(servesBlock(plainV1Beta(t)), func(c *handler.Config) { c.Run = (&runFunction{}).run }),
	}
	for want, config := range naming {
		cfg := baseConfig(t, &kittest.FakeResource{}, kittest.NewSigner(t, "kid-1", 7))
		config(&cfg)

		_, err := handler.New(cfg)

		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "run") {
			t.Errorf("handler.New error = %v, want a refusal naming run and %q", err, want)
		}
	}
}

func TestNew_AcceptsAManifestListingRunWithItsFunction(t *testing.T) {
	cfg := baseConfig(t, &kittest.FakeResource{}, kittest.NewSigner(t, "kid-1", 7))
	servesRun(t, &runFunction{})(&cfg)

	if _, err := handler.New(cfg); err != nil {
		t.Errorf("handler.New = %v, want a manifest listing run with Config.Run accepted", err)
	}
}

func TestRun_HandsTheRequestedRunToTheRunFunctionAndAnswersItsResultWithTheRequestsSecretsScrubbed(t *testing.T) {
	const secret = "github-token-s3cret"
	run := &runFunction{result: contract.RunResult{
		ExitCode: 0, Output: "added alice@example.com with " + secret + "\n", Digest: "sha256:5f1c",
		StartedAt: time.Date(2026, 11, 11, 14, 0, 9, 0, time.UTC), EndedAt: time.Date(2026, 11, 11, 14, 0, 10, 0, time.UTC),
	}}
	f := &kittest.FakeResource{}
	x := newHarness(t, f, servesRun(t, run))

	status, resp := x.answer(runFields(map[string]any{"secrets": map[string]any{"github": map[string]string{"token": secret}}}))

	want := contract.RunRequest{ID: "run_4c1e9a7f2b3d", Command: "php bin/add-user.php alice@example.com", Process: "web"}
	if len(run.requests) != 1 || run.requests[0] != want {
		t.Fatalf("the run function was handed %+v, want one call with %+v", run.requests, want)
	}
	if got := run.desired[0]; got.SubscriptionID != "sub-1" || got.Params["appName"] != "shop" {
		t.Errorf("the run function was handed Desired %+v, want the request's subscription and params", got)
	}
	if status != http.StatusOK || resp.Run == nil || resp.Pending {
		t.Fatalf("answered %d with %+v, want 200 carrying the run", status, resp)
	}
	r := resp.Run
	if r.ExitCode != 0 || r.Output != "added alice@example.com with [REDACTED]\n" || r.Digest != "sha256:5f1c" ||
		!r.StartedAt.Equal(run.result.StartedAt) || !r.EndedAt.Equal(run.result.EndedAt) {
		t.Errorf("run = %+v, want exit 0, the output with the secret scrubbed, the digest, the start and the end", r)
	}
	assertCalls(t, f, [3]int{})
}

func TestRun_AnswersPendingRefusedOrFailedAsTheRunFunctionDoes(t *testing.T) {
	const secret = "github-token-s3cret"
	cases := map[string]struct {
		err           error
		status        int
		pending       bool
		refusingClass string
	}{
		"a wait is a 200 answered pending with its detail": {handler.Wait("waiting for the host to report run_4c1e9a7f2b3d"), http.StatusOK, true, ""},
		"a refusal is a 400 with its class":                {handler.Refuse("run-refused", "the host refused it for server %s", secret), http.StatusBadRequest, false, "run-refused"},
		"an error is a 500":                                {errors.New("reading the host with " + secret), http.StatusInternalServerError, false, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			x := newHarness(t, &kittest.FakeResource{}, servesRun(t, &runFunction{err: tc.err}))

			status, raw := x.post(runFields(map[string]any{"secrets": map[string]any{"github": map[string]string{"token": secret}}}))

			resp := decodeResponse(t, raw)
			if status != tc.status || resp.Pending != tc.pending || resp.Run != nil || strings.Contains(string(raw), secret) {
				t.Errorf("answered %d with %s, want %d, pending %v, no run and the secret scrubbed", status, raw, tc.status, tc.pending)
			}
			if tc.pending && resp.StatusDetail != "waiting for the host to report run_4c1e9a7f2b3d" {
				t.Errorf("statusDetail = %q, want the wait's detail", resp.StatusDetail)
			}
			if tc.refusingClass != "" && handler.RefusedClass(resp.StatusDetail) != tc.refusingClass {
				t.Errorf("statusDetail = %q, want refused class %q", resp.StatusDetail, tc.refusingClass)
			}
		})
	}
}

func TestRun_AnswersAPanicInTheRunFunctionWithA500CarryingNothingOfIt(t *testing.T) {
	const secret = "github-token-s3cret"
	x := newHarness(t, &kittest.FakeResource{}, then(servesBlock(listsRun(t)), func(c *handler.Config) {
		c.Run = func(context.Context, contract.Desired, contract.RunRequest) (contract.RunResult, error) {
			panic(secret)
		}
	}))

	status, raw := x.post(runFields(nil))

	if status != http.StatusInternalServerError || strings.Contains(string(raw), secret) {
		t.Errorf("answered %d with %s, want a 500 carrying nothing of the panic", status, raw)
	}
}

func TestRun_AReSentRunReachesTheRunFunctionEveryTimeUnderTheSameIdempotencyKey(t *testing.T) {
	run := &runFunction{result: contract.RunResult{ExitCode: 0, Output: "added\n"}}
	x := newHarness(t, &kittest.FakeResource{}, servesRun(t, run))

	for i := 1; i <= 2; i++ {
		if status, resp := x.answer(runFields(nil)); status != http.StatusOK || resp.Run == nil {
			t.Fatalf("send %d answered %d with %+v, want 200 carrying the run", i, status, resp)
		}
	}

	if len(run.requests) != 2 {
		t.Errorf("the run function was called %d times for two sends, want 2, since each re-send reads the host's newest report", len(run.requests))
	}
}

func TestRun_ABlockWhoseManifestDoesNotListRunRefusesItWithoutCallingAnything(t *testing.T) {
	f := &kittest.FakeResource{}
	x := newHarness(t, f, servesBlock(plainV1Beta(t)))

	status, resp := x.answer(runFields(nil))

	if status != http.StatusBadRequest || handler.RefusedClass(resp.StatusDetail) != "run-not-listed" {
		t.Errorf("answered %d with %+v, want 400 refused run-not-listed", status, resp)
	}
	assertCalls(t, f, [3]int{})
}
