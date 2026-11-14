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

// Report is a Docker host's newest node report, read the same way by every block whose app runs on the host, each reader decoding its own part alone.
type Report struct {
	body    json.RawMessage
	absence *contract.Absence
}

// NewestReport reads the newest of the host subscription's node reports, found false while the host has sent none, except that a host the platform attests absent answers a report holding no volume without reading any.
func NewestReport(ctx context.Context, api observe.NodeReportsAPI, d contract.Desired, hostSubscription string) (Report, bool, error) {
	if hostSubscription == "" {
		return Report{}, false, handler.Refuse("host-binding", "the host binding carries no subscriptionId, so the host's node reports cannot be read")
	}
	if absence, ok := d.AttestedAbsent(hostSubscription); ok {
		return Report{body: json.RawMessage(`{"host":{"volumes":[]}}`), absence: &absence}, true, nil
	}
	reports, err := newestReports(ctx, api, d, hostSubscription)
	if err != nil {
		return Report{}, false, fmt.Errorf("reading the host's node reports: %w", err)
	}
	// A platform from before the limit answers every report it keeps, so the newest is still picked here.
	var newest *observe.NodeReport
	for i, r := range reports {
		if newest == nil || r.ReceivedAt.After(newest.ReceivedAt) {
			newest = &reports[i]
		}
	}
	if newest == nil {
		return Report{}, false, nil
	}
	report, err := ReadReport(newest.Body)
	if err != nil {
		return Report{}, false, fmt.Errorf("reading the host's newest node report: %w", err)
	}
	return report, true, nil
}

const newestReportOnly = 1

func newestReports(ctx context.Context, api observe.NodeReportsAPI, d contract.Desired, hostSubscription string) ([]observe.NodeReport, error) {
	if limited, ok := api.(observe.NewestNodeReportsAPI); ok {
		return limited.NewestNodeReports(ctx, d, hostSubscription, newestReportOnly)
	}
	return api.NodeReports(ctx, d, hostSubscription)
}

// ReadReport reads a node report body its caller already holds, such as the platform reading a host's newest report itself, and fails only a body that is not JSON.
func ReadReport(body []byte) (Report, error) {
	if err := json.Unmarshal(body, new(json.RawMessage)); err != nil {
		return Report{}, fmt.Errorf("decoding the host's node report: %w", err)
	}
	return Report{body: body}, nil
}

// reportedCapacity holds pointers, so a capacity missing a measure names it rather than counting it as zero.
type reportedCapacity struct {
	Host struct {
		Capacity struct {
			Allocatable *struct {
				CPUMillis   *int64 `json:"cpuMillis"`
				MemoryBytes *int64 `json:"memoryBytes"`
				DiskBytes   *int64 `json:"diskBytes"`
			} `json:"allocatable"`
			Error json.RawMessage `json:"error"`
		} `json:"capacity"`
	} `json:"host"`
}

const allocatablePath = "host.capacity.allocatable"

// capacity reads host.capacity alone, and a report naming neither a capacity nor an error yields none with an empty unread.
func (r Report) capacity() (allocatable Allocatable, given bool, unread string) {
	if len(r.body) == 0 {
		return Allocatable{}, false, ""
	}
	var parts reportedCapacity
	if err := r.Decode(&parts); err != nil {
		return Allocatable{}, false, misread(err) + ", which no amount can be read from"
	}
	reported := parts.Host.Capacity.Allocatable
	if reported == nil {
		return Allocatable{}, false, hostError(parts.Host.Capacity.Error)
	}
	measures := []struct {
		name   string
		amount *int64
	}{{"cpuMillis", reported.CPUMillis}, {"memoryBytes", reported.MemoryBytes}, {"diskBytes", reported.DiskBytes}}
	for _, m := range measures {
		if m.amount == nil {
			return Allocatable{}, false, allocatablePath + " lacks " + m.name
		}
		if *m.amount < 0 {
			return Allocatable{}, false, fmt.Sprintf("%s holds a negative %s, %d", allocatablePath, m.name, *m.amount)
		}
	}
	return Allocatable{CPUMillis: *reported.CPUMillis, MemoryBytes: *reported.MemoryBytes, DiskBytes: *reported.DiskBytes}, true, ""
}

// hostError quotes an error the host sent as anything but text as it stands, so its type never hides it.
func hostError(sent json.RawMessage) string {
	var text string
	if err := json.Unmarshal(sent, &text); err != nil {
		return string(sent)
	}
	return text
}

func misread(err error) string {
	var mistyped *json.UnmarshalTypeError
	switch {
	case !errors.As(err, &mistyped):
		return err.Error()
	case mistyped.Field == "":
		return "the report holds a JSON " + mistyped.Value
	}
	return fmt.Sprintf("%s holds a JSON %s", mistyped.Field, mistyped.Value)
}

// Allocatable is the capacity the report holds whole, with false when it holds none or one CapacityError explains.
func (r Report) Allocatable() (Allocatable, bool) {
	allocatable, given, _ := r.capacity()
	return allocatable, given
}

// CapacityError is why the report holds no allocatable capacity to size on, empty when it holds one whole or names no reason.
func (r Report) CapacityError() string {
	_, _, unread := r.capacity()
	return unread
}

// Decode reads the report's body into v, for the fields only one block reads.
func (r Report) Decode(v any) error {
	return json.Unmarshal(r.body, v)
}

type reportedAgent struct {
	Host struct {
		Agent struct {
			SyncedCommit json.RawMessage `json:"syncedCommit"`
		} `json:"agent"`
	} `json:"host"`
}

// SyncedCommit reads host.agent.syncedCommit, never the agent's log, and its error says why the report names none.
func (r Report) SyncedCommit() (string, error) {
	if r.absence != nil {
		return "", fmt.Errorf("the platform's %s read the host's %s absent, so it syncs no commit", r.absence.Observer, r.absence.Resource)
	}
	var parts reportedAgent
	if err := r.Decode(&parts); err != nil {
		return "", fmt.Errorf("the host's newest node report names no commit its agent synced, since %s", misread(err))
	}
	raw := parts.Host.Agent.SyncedCommit
	if raw == nil {
		return "", fmt.Errorf("the host's newest node report carries no host.agent.syncedCommit, so the host runs a hcloud-docker-host release before %s; upgrade the host to %s or later, whose report names the commit its agent last synced",
			SyncedCommitSince, SyncedCommitSince)
	}
	var commit *string
	if err := json.Unmarshal(raw, &commit); err != nil {
		return "", fmt.Errorf("the host's newest node report holds host.agent.syncedCommit as %s, which is not a commit: %w", raw, err)
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
		Apps     map[string][]Container   `json:"apps"`
		Releases map[string]releaseOutput `json:"releases"`
	} `json:"host"`
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
			if logs := led(c.Name, *c.Logs); len(logs) > 0 {
				lines = append(lines, logs...)
			} else {
				lines = append(lines, c.Name+" | wrote no output yet")
			}
		}
	}
	if !held {
		return []string{notOnHostYet}, nil
	}
	return lines, nil
}

// AppRelease is an app's newest release command as its host's newest node report records it, which the host keeps until the next release command runs.
type AppRelease struct {
	Failed        bool
	FailedDigest  string
	ServingDigest string
}

// Serves reports whether any image of the app serves, which a failed first deploy leaves none of.
func (a AppRelease) Serves() bool { return a.ServingDigest != "" }

// AppRelease reads app's newest release command and the digest its running container labelled with subscription serves, reading an app the report names no release of as not failed.
func (r Report) AppRelease(app, subscription string) (AppRelease, error) {
	read, _, err := r.appRelease(app, subscription)
	return read, err
}

func (r Report) appRelease(app, subscription string) (AppRelease, int, error) {
	var out reportedOutput
	if err := r.Decode(&out); err != nil {
		return AppRelease{}, 0, fmt.Errorf("decoding app %s's release from the host's newest node report: %w", app, err)
	}
	var read AppRelease
	ran := out.Host.Releases[app]
	if ran.ExitCode != 0 {
		read.Failed, read.FailedDigest = true, digestOf(ran.Image)
	}
	for _, c := range out.Host.Apps[app] {
		if c.Subscription == subscription && c.State == stateRunning {
			read.ServingDigest = digestOf(c.Image)
			break
		}
	}
	return read, ran.ExitCode, nil
}

// AskedRelease is an app's newest release command judged against the image the app's newest deploy asked for.
type AskedRelease struct {
	AppRelease
	AskedDigest string
	ExitCode    int
}

func (a AskedRelease) FailedOnAsked() bool {
	return a.Failed && (a.AskedDigest == "" || a.FailedDigest == a.AskedDigest)
}

// AskedAbsent reports whether a release failed on an older image while no container serves the asked one; once the asked image serves, that older failure reads as nothing.
func (a AskedRelease) AskedAbsent() bool {
	return a.Failed && !a.FailedOnAsked() && a.ServingDigest != a.AskedDigest
}

// ReleaseAgainst reads app's newest release command as AppRelease does and judges it against asked, the image ImageFile.Asked returns, so every reader of the record applies one rule.
func (r Report) ReleaseAgainst(app, subscription, asked string) (AskedRelease, error) {
	read, exitCode, err := r.appRelease(app, subscription)
	if err != nil {
		return AskedRelease{}, err
	}
	return AskedRelease{AppRelease: read, AskedDigest: digestOf(asked), ExitCode: exitCode}, nil
}

const (
	imageVariable          = "APP_IMAGE="
	rolledBackFromVariable = "APP_ROLLED_BACK_FROM="
)

// ImageFile is an app's image.env in the host's repository: the image the host runs the app on, the only line the host's agent reads, and after a rollback the image the rolled-back deploy asked for.
type ImageFile struct {
	Image          string
	RolledBackFrom string
}

// ParseImageFile reads an image file's text, skipping any line it does not define, as the host's agent does.
func ParseImageFile(text string) ImageFile {
	var f ImageFile
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, imageVariable):
			f.Image = strings.TrimPrefix(line, imageVariable)
		case strings.HasPrefix(line, rolledBackFromVariable):
			f.RolledBackFrom = strings.TrimPrefix(line, rolledBackFromVariable)
		}
	}
	return f
}

func (f ImageFile) String() string {
	text := imageVariable + f.Image + "\n"
	if f.RolledBackFrom != "" {
		text += rolledBackFromVariable + f.RolledBackFrom + "\n"
	}
	return text
}

// Asked is the image the app's newest deploy asked for: the one a rollback returned from when the file names one, and Image otherwise.
func (f ImageFile) Asked() string {
	if f.RolledBackFrom != "" {
		return f.RolledBackFrom
	}
	return f.Image
}

// digestOf is the digest an image reference pins, or the whole reference when it pins none.
func digestOf(image string) string {
	if _, digest, ok := strings.Cut(image, "@"); ok {
		return digest
	}
	return image
}

// led returns each non-empty line of text led by its container's name.
func led(container, text string) []string {
	var lines []string
	for line := range strings.Lines(text) {
		if line = strings.TrimRight(line, "\r\n"); line != "" {
			lines = append(lines, container+" | "+line)
		}
	}
	return lines
}
