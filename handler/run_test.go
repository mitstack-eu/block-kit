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

const runSecret = "github-token-s3cret"

type runFunction struct {
	requests []contract.RunRequest
	result   contract.RunResult
	err      error
}

func (r *runFunction) run(_ context.Context, _ contract.Desired, req contract.RunRequest) (contract.RunResult, error) {
	r.requests = append(r.requests, req)
	return r.result, r.err
}

func listsRun(t *testing.T) []byte {
	return withManifestEdit(t, plainV1Beta(t), "validate]", "validate, run]")
}

func runFields() map[string]any {
	return map[string]any{
		"operation": "run", "blockId": "plain", "appId": "app-1", "idempotencyKey": "sub-1:run:run_4c1e9a7f2b3d",
		"runId": "run_4c1e9a7f2b3d", "command": "php bin/add-user.php alice@example.com", "process": "web",
		"secrets": map[string]any{"github": map[string]string{"token": runSecret}},
	}
}

func TestNew_RefusesARunDeclarationWithoutItsFunction(t *testing.T) {
	naming := map[string]func(*handler.Config){
		"Config.Run":        servesBlock(listsRun(t)),
		"does not list run": then(servesBlock(plainV1Beta(t)), func(c *handler.Config) { c.Run = (&runFunction{}).run }),
	}
	for want, config := range naming {
		cfg := baseConfig(t, &kittest.FakeResource{}, kittest.NewSigner(t, "kid-1", 7))
		config(&cfg)

		if _, err := handler.New(cfg); err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "run") {
			t.Errorf("handler.New error = %v, want a refusal naming run and %q", err, want)
		}
	}
}

func TestRun_HandsEachSendToTheRunFunctionAndAnswersItsResultWithTheRequestsSecretsScrubbed(t *testing.T) {
	run := &runFunction{result: contract.RunResult{ExitCode: 3, Output: "added alice with " + runSecret + "\n", Digest: "sha256:5f1c"}}
	x := newHarness(t, &kittest.FakeResource{}, then(servesBlock(listsRun(t)), func(c *handler.Config) { c.Run = run.run }))

	x.answer(runFields())
	status, resp := x.answer(runFields())

	if want := (contract.RunRequest{ID: "run_4c1e9a7f2b3d", Command: "php bin/add-user.php alice@example.com", Process: "web"}); len(run.requests) != 2 || run.requests[1] != want {
		t.Errorf("two sends handed the run function %+v, want %+v on each, since each send reads the host's newest report", run.requests, want)
	}
	if status != http.StatusOK || resp.Run == nil || resp.Run.ExitCode != 3 || resp.Run.Output != "added alice with [REDACTED]\n" || resp.Run.Digest != "sha256:5f1c" {
		t.Errorf("answered %d with %+v, want 200 carrying exit 3, the digest and the output with the secret scrubbed", status, resp.Run)
	}
}

func TestRun_AnswersPendingRefusedOrFailedAsTheRunFunctionDoes(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
		class  string
	}{
		"a wait is a 200 answered pending":  {handler.Wait("waiting for the host to report run_4c1e9a7f2b3d"), http.StatusOK, ""},
		"a refusal is a 400 with its class": {handler.Refuse("run-refused", "the host refused it for server %s", runSecret), http.StatusBadRequest, "run-refused"},
		"an error is a 500":                 {errors.New("reading the host with " + runSecret), http.StatusInternalServerError, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			x := newHarness(t, &kittest.FakeResource{}, then(servesBlock(listsRun(t)), func(c *handler.Config) { c.Run = (&runFunction{err: tc.err}).run }))

			status, raw := x.post(runFields())

			resp := decodeResponse(t, raw)
			if pending := tc.status == http.StatusOK; status != tc.status || resp.Pending != pending || resp.Run != nil || strings.Contains(string(raw), runSecret) || handler.RefusedClass(resp.StatusDetail) != tc.class {
				t.Errorf("answered %d with %s, want %d, pending %v, refused class %q, no run and the secret scrubbed", status, raw, tc.status, tc.status == http.StatusOK, tc.class)
			}
		})
	}
}

func TestRun_AWaitBesideTheHostsStartAnswersPendingCarryingOnlyTheStartAndDigest(t *testing.T) {
	started := time.Date(2026, 11, 11, 14, 0, 30, 0, time.UTC)
	run := &runFunction{
		result: contract.RunResult{ExitCode: 3, Output: "half done with " + runSecret, Digest: "sha256:5f1c", StartedAt: started},
		err:    handler.Wait("the host is running run_4c1e9a7f2b3d and has not reported its end yet"),
	}
	x := newHarness(t, &kittest.FakeResource{}, then(servesBlock(listsRun(t)), func(c *handler.Config) { c.Run = run.run }))

	status, raw := x.post(runFields())

	resp := decodeResponse(t, raw)
	if status != http.StatusOK || !resp.Pending || resp.Run == nil || *resp.Run != (contract.RunResult{Digest: "sha256:5f1c", StartedAt: started}) {
		t.Errorf("answered %d with %s, want 200 pending carrying the run's start and digest alone", status, raw)
	}
	if strings.Contains(string(raw), "half done") || strings.Contains(string(raw), runSecret) {
		t.Errorf("answered %s, want no output from a run that has not ended", raw)
	}
}

func TestRun_ABlockWhoseManifestDoesNotListRunRefusesItWithoutCallingAnything(t *testing.T) {
	f := &kittest.FakeResource{}
	x := newHarness(t, f, servesBlock(plainV1Beta(t)))

	status, resp := x.answer(runFields())

	if status != http.StatusBadRequest || handler.RefusedClass(resp.StatusDetail) != "run-not-listed" {
		t.Errorf("answered %d with %+v, want 400 refused run-not-listed", status, resp)
	}
	assertCalls(t, f, [3]int{})
}
