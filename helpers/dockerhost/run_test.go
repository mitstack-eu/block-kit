package dockerhost_test

// A Runner commits one run request per run id into the app's directory of the host's repository, naming the host's server and a deadline 15 minutes on, and answers pending until the host's newest report lists the run, then finished or failed as the report says.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost/dockerhosttest"
)

const (
	runBlock    = "docker-app-slice"
	runApp      = "shop"
	runServer   = "61432178"
	runID       = "run_4c1e9a7f2b3d"
	runCommand  = "php bin/add-user.php alice@example.com"
	requestPath = runApp + "/" + runBlock + ".run-request"
)

var runPressed = time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC)

type runEnv struct {
	host   *dockerhosttest.Host
	runner dockerhost.Runner
}

func newRunEnv(t *testing.T, processes ...string) *runEnv {
	t.Helper()
	if len(processes) == 0 {
		processes = []string{"web"}
	}
	host := dockerhosttest.New(t, dbRepo, dbHostSub, runBlock, "web")
	host.Set(map[string]string{runApp + "/" + runBlock + ".compose.yaml": "services: {}\n"})
	return &runEnv{host: host, runner: dockerhost.Runner{
		Block: runBlock, Processes: processes, Reports: host, GitHub: host.GitHub, Now: func() time.Time { return runPressed },
	}}
}

func runDesired(host map[string]string) contract.Desired {
	binding := map[string]string{"clusterRepoUrl": "https://github.com/" + dbRepo, "subscriptionId": dbHostSub, "execKind": "git-pull", "execTarget": runServer}
	for k, v := range host {
		if v == "" {
			delete(binding, k)
			continue
		}
		binding[k] = v
	}
	return contract.Desired{
		Identity: contract.Identity{SubscriptionID: "sub_slice_shop", BlockID: runBlock, OrgID: "org_0123456789ab", AppID: "app_shop"},
		Params:   map[string]string{"appName": runApp},
		Secrets:  map[string]map[string]string{"github": {"token": dbToken, "owner": dbOwner}},
		Bindings: map[string]map[string]string{"host": binding},
	}
}

func (e *runEnv) run(t *testing.T, req contract.RunRequest) (contract.RunResult, error) {
	t.Helper()
	return e.runner.Run(context.Background(), runDesired(nil), req)
}

func asked(id string) contract.RunRequest {
	return contract.RunRequest{ID: id, Command: runCommand}
}

func readGolden(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("testdata/%s: %v", name, err)
	}
	return m
}

func goldenRuns(t *testing.T) []map[string]any {
	t.Helper()
	var report struct {
		Host struct {
			Runs []map[string]any `json:"runs"`
		} `json:"host"`
	}
	raw, err := os.ReadFile("testdata/node-report-runs.json")
	if err == nil {
		err = json.Unmarshal(raw, &report)
	}
	if err != nil || len(report.Host.Runs) == 0 {
		t.Fatalf("testdata/node-report-runs.json holds no runs: %v", err)
	}
	return report.Host.Runs
}

func (e *runEnv) committedRequest(t *testing.T) map[string]any {
	t.Helper()
	content, ok := e.host.Main()[requestPath]
	if !ok {
		t.Fatalf("main holds no %s; want the run request committed there", requestPath)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", requestPath, err, content)
	}
	return m
}

func wantPending(t *testing.T, err error) {
	t.Helper()
	var w *handler.Waiting
	if !errors.As(err, &w) {
		t.Fatalf("Run error = %v, want a wait answered pending", err)
	}
}

func wantRefused(t *testing.T, err error, class string, names ...string) {
	t.Helper()
	var r *handler.Refused
	if !errors.As(err, &r) || r.Class != class {
		t.Fatalf("Run error = %v, want a refusal of class %q", err, class)
	}
	for _, name := range names {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("Run error = %q, want it to name %q", err, name)
		}
	}
}

func TestRunner_CommitsOneRequestNamingTheServerAndADeadline15MinutesOnAndAnswersPending(t *testing.T) {
	e := newRunEnv(t)
	before := e.host.Head()

	_, err := e.run(t, contract.RunRequest{ID: runID, Command: runCommand, Process: "web"})

	wantPending(t, err)
	if got, want := e.committedRequest(t), readGolden(t, "run-request.json"); !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want the golden testdata/run-request.json %v", requestPath, got, want)
	}
	if e.host.Head() == before {
		t.Error("main's head did not move, want one commit of the run request")
	}
	if msg := e.host.Message("main"); !strings.Contains(msg, runID) || !strings.Contains(msg, "Mitstack-Subscription: sub_slice_shop") {
		t.Errorf("commit message = %q, want it to name %s and the subscription", msg, runID)
	}
}

func TestRunner_RequestPathNamesTheFileARunCommitsSoABlockCanRemoveIt(t *testing.T) {
	e := newRunEnv(t)
	_, err := e.run(t, asked(runID))
	wantPending(t, err)

	got := e.runner.RequestPath(runApp)

	if _, held := e.host.Main()[got]; got != requestPath || !held {
		t.Errorf("RequestPath(%s) = %q, held on main %v; want %q, the file the run committed", runApp, got, held, requestPath)
	}
}

func TestRunner_CommitsNothingWhenTheSameRunIdIsSentAgain(t *testing.T) {
	e := newRunEnv(t)
	_, first := e.run(t, asked(runID))
	wantPending(t, first)
	committed := e.host.Head()

	_, again := e.run(t, asked(runID))

	wantPending(t, again)
	if e.host.Head() != committed {
		t.Error("the re-sent run moved main's head, want nothing committed again for the same run id")
	}
}

func (e *runEnv) runReporting(t *testing.T, platform *dockerhosttest.Platform, req contract.RunRequest) error {
	t.Helper()
	_, err := e.runner.Run(platform.Context(context.Background()), runDesired(nil), req)
	return err
}

func TestRunner_ReportsTheCommitOfItsRequestOnceNamingTheHostsRepositoryWithTheCallbackToken(t *testing.T) {
	e := newRunEnv(t)
	platform := dockerhosttest.NewPlatform(t)

	err := e.runReporting(t, platform, asked(runID))

	wantPending(t, err)
	want := []dockerhosttest.CommitReport{{Repository: dbRepo, Commit: e.host.Head(), Bearer: dockerhosttest.CallbackToken}}
	if got := platform.Reports(); !reflect.DeepEqual(got, want) {
		t.Errorf("commit reports = %+v, want %+v, one for the commit the run landed", got, want)
	}
}

func TestRunner_ReportsNothingWhenTheSameRunIdIsSentAgainAndNothingIsCommitted(t *testing.T) {
	e := newRunEnv(t)
	platform := dockerhosttest.NewPlatform(t)
	wantPending(t, e.runReporting(t, platform, asked(runID)))

	err := e.runReporting(t, platform, asked(runID))

	wantPending(t, err)
	if got := platform.Reports(); len(got) != 1 {
		t.Errorf("commit reports = %+v, want the first run's one alone, since the second committed nothing", got)
	}
}

func TestRunner_KeepsItsCommitAndAnswersPendingWhenThePlatformAnswers503ToItsReportAndLogsTheFailure(t *testing.T) {
	e := newRunEnv(t)
	platform := dockerhosttest.NewPlatform(t)
	platform.Status = http.StatusServiceUnavailable
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	before := e.host.Head()

	err := e.runReporting(t, platform, asked(runID))

	wantPending(t, err)
	if e.host.Head() == before {
		t.Error("main's head did not move, want the run request committed though its report failed")
	}
	if got := platform.Reports(); len(got) != 1 {
		t.Errorf("commit reports = %+v, want one request and no retry", got)
	}
	if line := logged.String(); !strings.Contains(line, e.host.Head()) || !strings.Contains(line, "503") || strings.Contains(line, dockerhosttest.CallbackToken) {
		t.Errorf("log = %q, want a line naming the commit and the 503, carrying no token", line)
	}
}

func TestRunner_FindsARequestAlreadyOnMainAfterTheBlockFailedBeforeItsAnswerAndCommitsNothing(t *testing.T) {
	e := newRunEnv(t)
	golden, err := os.ReadFile("testdata/run-request.json")
	if err != nil {
		t.Fatal(err)
	}
	e.host.Set(map[string]string{requestPath: string(golden)})
	committed := e.host.Head()
	restarted := dockerhost.Runner{Block: runBlock, Processes: []string{"web"}, Reports: e.host, GitHub: e.host.GitHub, Now: func() time.Time { return runPressed.Add(time.Minute) }}

	_, err = restarted.Run(context.Background(), runDesired(nil), asked(runID))

	wantPending(t, err)
	if e.host.Head() != committed {
		t.Error("a restarted block moved main's head, want the request already on main found and nothing committed")
	}
}

func TestRunner_ReplacesTheRequestOfAnEarlierRunWithOneCommit(t *testing.T) {
	e := newRunEnv(t)
	_, first := e.run(t, asked("run_000000000001"))
	wantPending(t, first)
	earlier := e.host.Head()

	_, err := e.run(t, asked(runID))

	wantPending(t, err)
	if got := e.committedRequest(t)["runId"]; got != runID || e.host.Head() == earlier {
		t.Errorf("after a new run id the request names %v at a head that moved %v, want %s in a new commit", got, e.host.Head() != earlier, runID)
	}
}

func TestRunner_StaysPendingAfterItsCommitWhileTheHostHasSentNoReport(t *testing.T) {
	e := newRunEnv(t)
	e.host.Silent = true

	_, err := e.run(t, asked(runID))

	wantPending(t, err)
	if got := e.committedRequest(t)["runId"]; got != runID {
		t.Errorf("request runId = %v, want %s committed while the host is silent", got, runID)
	}
}

func TestRunner_CommitsTheRequestOnceAfterMainMovedUnderItsFirstCommit(t *testing.T) {
	e := newRunEnv(t)
	moved := false
	e.host.BeforeWrite(func(method, _ string) error {
		if method == "PATCH" && !moved {
			moved = true
			e.host.Set(map[string]string{"other/compose.yaml": "services: {}\n"})
		}
		return nil
	})

	_, err := e.run(t, asked(runID))

	wantPending(t, err)
	if got := e.committedRequest(t)["runId"]; !moved || got != runID {
		t.Errorf("after main moved under the commit, the request names %v, want %s committed on the newest main", got, runID)
	}
}

func TestRunner_RefusesARunOnAHostThePlatformAttestsAbsentAndCommitsNothing(t *testing.T) {
	e := newRunEnv(t)
	d := runDesired(nil)
	d.Absences = map[string]contract.Absence{"host": {Observer: "hcloud-docker-host", Resource: "server", ReadAbsentAt: runPressed, ProvesAbsence: true}}

	_, err := e.runner.Run(context.Background(), d, asked(runID))

	wantRefused(t, err, "host-absent", "absent")
	if writes := e.host.Writes(); len(writes) != 0 {
		t.Errorf("Run wrote %v, want nothing written for a host the platform attests absent", writes)
	}
}

func TestRunner_StaysPendingWhileTheNewestReportLacksTheRunId(t *testing.T) {
	e := newRunEnv(t)
	e.host.Runs = goldenRuns(t)

	_, err := e.run(t, asked("run_aaaaaaaaaaaa"))

	wantPending(t, err)
}

func TestRunner_StaysPendingWhileTheNewestReportListsTheRunWithNoExitCodeOrRefusal(t *testing.T) {
	e := newRunEnv(t)
	e.host.Runs = goldenRuns(t)

	_, err := e.run(t, asked("run_e3a8c5f10b27"))

	wantPending(t, err)
}

func TestRunner_AnswersFinishedWithTheExitCodeAndOutputOnceTheNewestReportListsTheRun(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want contract.RunResult
	}{
		{"a run that exited 0", runID, contract.RunResult{ExitCode: 0, Output: "added alice@example.com\n", Digest: "sha256:5f1c",
			StartedAt: time.Date(2026, 11, 11, 14, 0, 9, 0, time.UTC), EndedAt: time.Date(2026, 11, 11, 14, 0, 10, 0, time.UTC)}},
		{"a run whose command exited 1", "run_7b0c2d9e4f13", contract.RunResult{ExitCode: 1, Output: "no such table: users\n", Digest: "sha256:5f1c",
			StartedAt: time.Date(2026, 11, 11, 13, 40, 2, 0, time.UTC), EndedAt: time.Date(2026, 11, 11, 13, 40, 3, 0, time.UTC)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRunEnv(t)
			e.host.Runs = goldenRuns(t)
			before := e.host.Head()

			got, err := e.run(t, asked(tc.id))

			if err != nil {
				t.Fatalf("Run(%s) error = %v, want finished", tc.id, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Run(%s) = %+v, want %+v", tc.id, got, tc.want)
			}
			if e.host.Head() != before {
				t.Errorf("Run(%s) committed once the report listed the run, want nothing committed", tc.id)
			}
		})
	}
}

func TestRunner_AnswersTheWholeOutputTheNewestReportCarries(t *testing.T) {
	e := newRunEnv(t)
	long := strings.Repeat("row added\n", 7000)
	e.host.Runs = []map[string]any{{"runId": runID, "exitCode": 0, "output": long}}

	got, err := e.run(t, asked(runID))

	if err != nil || got.Output != long {
		t.Errorf("Run = %d bytes of output, %v; want all %d bytes the report carries", len(got.Output), err, len(long))
	}
}

func TestRunner_AnswersFailedNamingTheHostsRefusalOnceTheNewestReportListsTheRunAsRefused(t *testing.T) {
	e := newRunEnv(t)
	e.host.Runs = goldenRuns(t)

	_, err := e.run(t, asked("run_9d2b7e4a1c0f"))

	wantRefused(t, err, "run-refused", "run_9d2b7e4a1c0f", "the request names server 61432178, and this host is server 70011223")
}

func TestRunner_RefusesAProcessTheBlockDoesNotRunListingThoseItRunsAndCommitsNothing(t *testing.T) {
	e := newRunEnv(t, "web", "worker")

	_, err := e.run(t, contract.RunRequest{ID: runID, Command: runCommand, Process: "queue"})

	wantRefused(t, err, "run-process", `"queue"`, "web", "worker")
	if writes := e.host.Writes(); len(writes) != 0 {
		t.Errorf("a run naming a process the block does not run wrote %v, want nothing written", writes)
	}
}

func TestRunner_RunsTheNamedProcessOrTheEntryWhenTheRunNamesNone(t *testing.T) {
	for _, tc := range []struct{ process, service string }{{"", "web"}, {"worker", "worker"}} {
		t.Run("process "+tc.process, func(t *testing.T) {
			e := newRunEnv(t, "web", "worker")

			_, err := e.run(t, contract.RunRequest{ID: runID, Command: runCommand, Process: tc.process})

			wantPending(t, err)
			if got := e.committedRequest(t)["service"]; got != tc.service {
				t.Errorf("a run naming process %q committed service %v, want %s", tc.process, got, tc.service)
			}
		})
	}
}

func TestRunner_RefusesAHostThatCannotTakeARunRequestAndCommitsNothing(t *testing.T) {
	cases := []struct {
		name  string
		host  map[string]string
		names []string
	}{
		{"a host binding with no execTarget", map[string]string{"execTarget": ""}, []string{"execTarget"}},
		{"a host that is not reached through its repository", map[string]string{"execKind": "ssh"}, []string{"execKind", `"ssh"`, "git-pull"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRunEnv(t)

			_, err := e.runner.Run(context.Background(), runDesired(tc.host), asked(runID))

			wantRefused(t, err, "host-binding", tc.names...)
			if writes := e.host.Writes(); len(writes) != 0 {
				t.Errorf("Run wrote %v, want nothing written", writes)
			}
		})
	}
}

func TestRunRequestFile_EncodesAsTheGoldenRequestFile(t *testing.T) {
	file := dockerhost.RunRequestFile{RunID: runID, ServerID: runServer, Deadline: runPressed.Add(dockerhost.RunDeadline), Service: "web", Command: runCommand}

	raw, err := json.Marshal(file)

	var got map[string]any
	if err == nil {
		err = json.Unmarshal(raw, &got)
	}
	if err != nil || !reflect.DeepEqual(got, readGolden(t, "run-request.json")) {
		t.Errorf("RunRequestFile encodes as %s, %v; want the golden testdata/run-request.json", raw, err)
	}
}

func TestReportedRun_DecodesEachEntryOfTheGoldenRuns(t *testing.T) {
	zero := 0
	raw, err := os.ReadFile("testdata/node-report-runs.json")
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Host struct {
			Runs []dockerhost.ReportedRun `json:"runs"`
		} `json:"host"`
	}

	err = json.Unmarshal(raw, &report)

	if err != nil || len(report.Host.Runs) != 4 {
		t.Fatalf("decoding the golden runs = %d entries, %v; want 4", len(report.Host.Runs), err)
	}
	want := dockerhost.ReportedRun{RunID: runID, Digest: "sha256:5f1c", StartedAt: time.Date(2026, 11, 11, 14, 0, 9, 0, time.UTC),
		EndedAt: time.Date(2026, 11, 11, 14, 0, 10, 0, time.UTC), ExitCode: &zero, Output: "added alice@example.com\n"}
	if got := report.Host.Runs[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("the finished run decodes as %+v, want %+v", got, want)
	}
	if refused := report.Host.Runs[2]; refused.ExitCode != nil || refused.Refusal == "" {
		t.Errorf("the refused run decodes as %+v, want no exit code and the refusal", refused)
	}
	if running := report.Host.Runs[3]; running.ExitCode != nil || running.Refusal != "" || running.StartedAt.IsZero() {
		t.Errorf("the running run decodes as %+v, want a start and neither an exit code nor a refusal", running)
	}
}
