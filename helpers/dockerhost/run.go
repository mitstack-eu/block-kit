package dockerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
)

// RunDeadline is how long after its commit a host may start a run, the release command's limit, so a request the host finds later is refused rather than run late.
const RunDeadline = 15 * time.Minute

const (
	runRequestSuffix = ".run-request"
	execKindGitPull  = "git-pull"
	classRunProcess  = "run-process"
	classRunRefused  = "run-refused"
	classHostBinding = "host-binding"
)

// RunRequestFile is what <block>.run-request in an app's directory of the host's repository holds for the host's agent to start one run.
type RunRequestFile struct {
	RunID    string    `json:"runId"`
	ServerID string    `json:"serverId"`
	Deadline time.Time `json:"deadline"`
	Service  string    `json:"service"`
	Command  string    `json:"command"`
}

// ReportedRun is one entry of host.runs in a Docker host's node report: a run that ended carries ExitCode, one the host refused carries Refusal, and one still running carries neither.
type ReportedRun struct {
	RunID     string    `json:"runId"`
	Digest    string    `json:"digest,omitempty"`
	StartedAt time.Time `json:"startedAt,omitzero"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
	ExitCode  *int      `json:"exitCode,omitempty"`
	Output    string    `json:"output,omitempty"`
	Refusal   string    `json:"refusal,omitempty"`
}

// Runner answers a block's run on a Docker host, committing one request per run id into the app's directory and reading the answer from the host's newest node report.
type Runner struct {
	// Block is the block's id, which names <block>.run-request beside the block's compose file.
	Block string
	// Processes are the compose services the block runs, its entry first, which a run naming no process runs.
	Processes []string
	Reports   observe.NodeReportsAPI
	// GitHub makes the client of the host's repository, nil for one that waits 30 seconds at most.
	GitHub func(base, token string) *ghapi.Client
	Now    func() time.Time
}

type reportedRuns struct {
	Host struct {
		Runs []ReportedRun `json:"runs"`
	} `json:"host"`
}

// Run answers from the host's newest report once it lists the run, and otherwise commits the request unless main already holds it and answers pending.
func (r Runner) Run(ctx context.Context, d contract.Desired, req contract.RunRequest) (contract.RunResult, error) {
	file, t, err := r.request(d, req)
	if err != nil {
		return contract.RunResult{}, err
	}
	ran, listed, err := reportedRun(ctx, r.Reports, d, t.hostSub, req.ID)
	if err != nil {
		return contract.RunResult{}, err
	}
	if listed {
		return answerRun(ran)
	}
	if err := retryOnMovedMain(func() error { return r.ask(ctx, d, t, file) }); err != nil {
		return contract.RunResult{}, err
	}
	return contract.RunResult{}, handler.Wait("run %s is in the host's repository and waits for the host's node report to list it", req.ID)
}

// request refuses, before anything is read, a process the block does not run and a host the request could not reach or the platform attests absent.
func (r Runner) request(d contract.Desired, req contract.RunRequest) (RunRequestFile, target, error) {
	service, err := r.service(req.Process)
	if err != nil {
		return RunRequestFile{}, target{}, err
	}
	t, err := readTarget(d)
	if err != nil {
		return RunRequestFile{}, target{}, err
	}
	if absence, ok := d.AttestedAbsent(t.hostSub); ok {
		return RunRequestFile{}, target{}, handler.Refuse("host-absent", "the platform's %s read the host's %s absent, so run %s has no host to run on; give the app a running host",
			absence.Observer, absence.Resource, req.ID)
	}
	host := d.Bindings[reqHost]
	if kind := host["execKind"]; kind != execKindGitPull {
		return RunRequestFile{}, target{}, handler.Refuse(classHostBinding,
			"the host's execKind is %q, but a run request reaches only a host whose agent pulls its repository, %s", kind, execKindGitPull)
	}
	if host["execTarget"] == "" {
		return RunRequestFile{}, target{}, handler.Refuse(classHostBinding,
			"the host binding carries no execTarget, the host's server id, so a run request could name no server to run on")
	}
	return RunRequestFile{
		RunID: req.ID, ServerID: host["execTarget"], Deadline: r.now().Add(RunDeadline).UTC().Truncate(time.Second),
		Service: service, Command: req.Command,
	}, t, nil
}

func (r Runner) service(process string) (string, error) {
	if len(r.Processes) == 0 {
		return "", errors.New("dockerhost: the Runner names no process, so a run has no service to start")
	}
	if process == "" {
		return r.Processes[0], nil
	}
	if !slices.Contains(r.Processes, process) {
		return "", handler.Refuse(classRunProcess, "the run names process %q, which this block does not run; it runs %s", process, strings.Join(r.Processes, ", "))
	}
	return process, nil
}

func (r Runner) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func reportedRun(ctx context.Context, api observe.NodeReportsAPI, d contract.Desired, hostSub, id string) (ReportedRun, bool, error) {
	report, found, err := NewestReport(ctx, api, d, hostSub)
	if err != nil || !found {
		return ReportedRun{}, false, err
	}
	var out reportedRuns
	if err := report.Decode(&out); err != nil {
		return ReportedRun{}, false, fmt.Errorf("decoding the runs of the host's newest node report: %w", err)
	}
	for _, ran := range out.Host.Runs {
		if ran.RunID == id {
			return ran, true, nil
		}
	}
	return ReportedRun{}, false, nil
}

func answerRun(ran ReportedRun) (contract.RunResult, error) {
	switch {
	case ran.Refusal != "":
		return contract.RunResult{}, handler.Refuse(classRunRefused, "the host refused run %s: %s", ran.RunID, ran.Refusal)
	case ran.ExitCode == nil:
		return contract.RunResult{}, handler.Wait("the host is running run %s and has not reported its end yet", ran.RunID)
	}
	return contract.RunResult{ExitCode: *ran.ExitCode, Output: ran.Output, Digest: ran.Digest, StartedAt: ran.StartedAt, EndedAt: ran.EndedAt}, nil
}

// ask finds a request for the same run id already on main, as a block that failed between its commit and its answer left it, and commits nothing then.
func (r Runner) ask(ctx context.Context, d contract.Desired, t target, file RunRequestFile) error {
	repo, err := dialHostRepo(d, "", r.GitHub)
	if err != nil {
		return err
	}
	snap, err := repo.snapshot(ctx)
	if err != nil {
		return err
	}
	path := appPath(t.app, r.Block+runRequestSuffix)
	held, err := heldRunID(ctx, repo, snap, path)
	if err != nil || held == file.RunID {
		return err
	}
	content, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding run request %s: %w", file.RunID, err)
	}
	message := fmt.Sprintf("%s asks the host to run %s in %s\n\nMitstack-Subscription: %s\n", r.Block, file.RunID, t.app, t.subID)
	return repo.commit(ctx, snap.head, message, map[string]string{path: string(content) + "\n"})
}

// heldRunID is "" for a request file that does not parse, which the next run replaces, since its host could not have read it either.
func heldRunID(ctx context.Context, repo *hostRepo, snap snapshot, path string) (string, error) {
	if !snap.paths[path] {
		return "", nil
	}
	content, err := repo.read(ctx, snap.head, path)
	if err != nil {
		return "", err
	}
	var held RunRequestFile
	if json.Unmarshal([]byte(content), &held) != nil {
		return "", nil
	}
	return held.RunID, nil
}
