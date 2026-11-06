package dockerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
)

// SyncedCommitSince is the first hcloud-docker-host release whose node report names the commit its agent last synced.
const SyncedCommitSince = "0.8.0"

// Report is a Docker host's newest node report, read the same way by every block whose app runs on the host.
type Report struct {
	Allocatable *Reservation
	body        json.RawMessage
	agent       map[string]json.RawMessage
	absence     *contract.Absence
}

type reportParts struct {
	Host struct {
		Capacity struct {
			Allocatable *Reservation `json:"allocatable"`
		} `json:"capacity"`
		Agent map[string]json.RawMessage `json:"agent"`
	} `json:"host"`
}

// NewestReport reads the newest of the host subscription's node reports, found false while the host has sent none, except that a host the platform attests absent answers a report holding no volume without reading any.
func NewestReport(ctx context.Context, api observe.NodeReportsAPI, d contract.Desired, hostSubscription string) (Report, bool, error) {
	if hostSubscription == "" {
		return Report{}, false, handler.Refuse("host-binding", "the host binding carries no subscriptionId, so the host's node reports cannot be read")
	}
	if absence, ok := d.AttestedAbsent(hostSubscription); ok {
		return Report{body: json.RawMessage(`{"host":{"volumes":[]}}`), absence: &absence}, true, nil
	}
	reports, err := api.NodeReports(ctx, d, hostSubscription)
	if err != nil {
		return Report{}, false, fmt.Errorf("reading the host's node reports: %w", err)
	}
	var newest *observe.NodeReport
	for i, r := range reports {
		if newest == nil || r.ReceivedAt.After(newest.ReceivedAt) {
			newest = &reports[i]
		}
	}
	if newest == nil {
		return Report{}, false, nil
	}
	var parts reportParts
	if err := json.Unmarshal(newest.Body, &parts); err != nil {
		return Report{}, false, fmt.Errorf("decoding the host's newest node report: %w", err)
	}
	return Report{Allocatable: parts.Host.Capacity.Allocatable, body: newest.Body, agent: parts.Host.Agent}, true, nil
}

// Decode reads the report's body into v, for the fields only one block reads.
func (r Report) Decode(v any) error {
	return json.Unmarshal(r.body, v)
}

// SyncedCommit reads host.agent.syncedCommit, never the agent's log, and its error says why the report names none.
func (r Report) SyncedCommit() (string, error) {
	if r.absence != nil {
		return "", fmt.Errorf("the platform's %s read the host's %s absent, so it syncs no commit", r.absence.Observer, r.absence.Resource)
	}
	raw, ok := r.agent["syncedCommit"]
	if !ok {
		return "", fmt.Errorf("the host's newest node report carries no host.agent.syncedCommit, so the host runs a hcloud-docker-host release before %s; upgrade the host to %s or later, whose report names the commit its agent last synced",
			SyncedCommitSince, SyncedCommitSince)
	}
	var commit *string
	if err := json.Unmarshal(raw, &commit); err != nil {
		return "", fmt.Errorf("the host's newest node report gives host.agent.syncedCommit as %s, which is not a commit: %w", raw, err)
	}
	if commit == nil || *commit == "" {
		return "", errors.New("the host's agent has not synced since it took its current release, so the commit it runs cannot be read until its next sync")
	}
	return *commit, nil
}

const (
	noReportYet  = "The host has sent no node report yet, so this part's output shows once its first report arrives."
	noOutputYet  = "The host's newest node report carries no container's output, so its hcloud-docker-host release predates it; upgrade the host to the release whose node report carries each app container's output and its last release command's output."
	notOnHostYet = "The host's newest node report names no container of this part, so it has not started on the host yet."
)

// reportedOutput is each app's output as the host reports it, every container and release labelled with the subscription whose block wrote it.
type reportedOutput struct {
	Host struct {
		Apps     map[string][]containerOutput `json:"apps"`
		Releases map[string]releaseOutput     `json:"releases"`
	} `json:"host"`
}

// containerOutput's Logs is absent from the report of a host release that sends no container output, and Subscription is empty for a container no block labelled.
type containerOutput struct {
	Container    string  `json:"container"`
	Subscription string  `json:"subscription"`
	Image        string  `json:"image"`
	State        string  `json:"state"`
	Logs         *string `json:"logs"`
}

type releaseOutput struct {
	Subscription string `json:"subscription"`
	Container    string `json:"container"`
	Image        string `json:"image"`
	ExitCode     int    `json:"exitCode"`
	Output       string `json:"output"`
}

// SubscriptionLogs reads, from the newest node report of the host hostSubscription names, the last release command's output and the lines of each container labelled with d's subscription, each led by its container's name, and answers a line saying why when there are none, so an empty answer never reads as a part that wrote nothing.
func SubscriptionLogs(ctx context.Context, api observe.NodeReportsAPI, d contract.Desired, hostSubscription string) ([]string, error) {
	report, found, err := NewestReport(ctx, api, d, hostSubscription)
	if err != nil {
		return nil, err
	}
	if !found {
		return []string{noReportYet}, nil
	}
	var out reportedOutput
	if err := report.Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding the host's newest node report: %w", err)
	}
	owns := func(label string) bool { return label != "" && label == d.SubscriptionID }
	var lines []string
	for _, app := range slices.Sorted(maps.Keys(out.Host.Releases)) {
		if r := out.Host.Releases[app]; owns(r.Subscription) {
			lines = append(lines, fmt.Sprintf("%s | the release command exited %d on %s", r.Container, r.ExitCode, r.Image))
			lines = append(lines, led(r.Container, r.Output)...)
		}
	}
	held := len(lines) > 0
	for _, app := range slices.Sorted(maps.Keys(out.Host.Apps)) {
		for _, c := range out.Host.Apps[app] {
			if c.Logs == nil {
				return []string{noOutputYet}, nil
			}
			if !owns(c.Subscription) {
				continue
			}
			held = true
			if logs := led(c.Container, *c.Logs); len(logs) > 0 {
				lines = append(lines, logs...)
			} else {
				lines = append(lines, c.Container+" | wrote no output yet")
			}
		}
	}
	if !held {
		return []string{notOnHostYet}, nil
	}
	return lines, nil
}

// AppRelease is an app's newest release command as its host's newest node report gives it, which the host keeps until the next release command runs.
type AppRelease struct {
	Failed        bool
	FailedDigest  string
	ServingDigest string
}

// Serves reports whether any image of the app serves, which a failed first deploy leaves none of.
func (a AppRelease) Serves() bool { return a.ServingDigest != "" }

// AppRelease reads app's newest release command and the digest its running container labelled with subscription serves, reading an app the report names no release of as not failed.
func (r Report) AppRelease(app, subscription string) (AppRelease, error) {
	var out reportedOutput
	if err := r.Decode(&out); err != nil {
		return AppRelease{}, fmt.Errorf("decoding app %s's release from the host's newest node report: %w", app, err)
	}
	var read AppRelease
	if ran, ok := out.Host.Releases[app]; ok && ran.ExitCode != 0 {
		read.Failed, read.FailedDigest = true, digestOf(ran.Image)
	}
	for _, c := range out.Host.Apps[app] {
		if c.Subscription == subscription && c.State == "running" {
			read.ServingDigest = digestOf(c.Image)
			break
		}
	}
	return read, nil
}

// digestOf is the digest an image reference pins, or the whole reference when it pins none.
func digestOf(image string) string {
	if _, digest, ok := strings.Cut(image, "@"); ok {
		return digest
	}
	return image
}

// led gives each non-empty line of text led by its container's name.
func led(container, text string) []string {
	var lines []string
	for line := range strings.Lines(text) {
		if line = strings.TrimRight(line, "\r\n"); line != "" {
			lines = append(lines, container+" | "+line)
		}
	}
	return lines
}
