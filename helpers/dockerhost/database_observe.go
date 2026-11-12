package dockerhost

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

// The causes a database answers once it has read active, which its catalogue type lists word for word.
const (
	DatabaseUnhealthy  = "the database's container reads unhealthy on the host"
	DatabaseNotRunning = "the database's container is not running on the host"
)

// databaseReport is the host's newest node report, with the parts only a Database reads.
type databaseReport struct {
	Report
	Host struct {
		Apps    map[string][]Container `json:"apps"`
		Backups struct {
			Error *string `json:"error"`
		} `json:"backups"`
	} `json:"host"`
}

func (db Database) readReport(ctx context.Context, d contract.Desired, hostSub string) (databaseReport, bool, error) {
	report, found, err := NewestReport(ctx, db.Reports, d, hostSub)
	if err != nil || !found {
		return databaseReport{}, found, err
	}
	body := databaseReport{Report: report}
	if err := report.Decode(&body); err != nil {
		return databaseReport{}, false, fmt.Errorf("decoding the host's newest node report: %w", err)
	}
	return body, true, nil
}

// Observers reads the database's resource.
func (db Database) Observers() map[string]handler.Observer {
	return map[string]handler.Observer{DatabaseResource: db}
}

// SubscriptionLogs reads the database container's lines from its host's newest node report.
func (db Database) SubscriptionLogs(ctx context.Context, d contract.Desired) ([]string, error) {
	t, err := readTarget(d)
	if err != nil {
		return nil, err
	}
	return SubscriptionLogs(ctx, db.Reports, d, t.hostSub)
}

func (h databaseReport) database(name string, app string) (Container, bool) {
	for _, c := range h.Host.Apps[app] {
		if c.Name == name {
			return c, true
		}
	}
	return Container{}, false
}

// ObserveResource waits on the host's newest node report, since files at main's head show only intent, and a database
// that has read active stays present, carrying Unhealthy, when its container stops or turns unhealthy.
func (db Database) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	t, err := readTarget(r.Desired)
	if err != nil {
		return contract.Observation{}, err
	}
	repo, err := db.dial(r.Desired)
	if err != nil {
		return contract.Observation{}, err
	}
	snap, err := repo.snapshot(ctx)
	if err != nil {
		return contract.Observation{}, err
	}
	owner, err := db.owner(ctx, repo, snap, t.app)
	if err != nil {
		return contract.Observation{}, err
	}
	landed := db.landedFiles()
	var missing []string
	for _, name := range landed {
		if !snap.paths[appPath(t.app, name)] {
			missing = append(missing, name)
		}
	}
	switch {
	case len(missing) == len(landed) || heldByAnother(owner, t):
		return contract.Observation{State: contract.Absent}, nil
	case len(missing) > 0:
		return unknown("the host's repository holds %s's database without %s", t.app, strings.Join(missing, " and ")), nil
	}
	report, found, err := db.readReport(ctx, r.Desired, t.hostSub)
	if err != nil {
		return contract.Observation{}, err
	}
	if !found {
		return unknown("the host has sent no node report, so its pull agent's sync of %s cannot be read", snap.head), nil
	}
	last, err := report.SyncedCommit()
	if err != nil {
		return unknown("%s, so whether it runs main's head %s cannot be read", err, snap.head), nil
	}
	if last != snap.head {
		changed, err := db.changedSince(ctx, repo, t.app, last, snap.head)
		if err != nil {
			return unknown("the host's pull agent last synced %q, not main's head %s, and whether the database's files changed since cannot be read: %v",
				last, snap.head, err), nil
		}
		if changed != "" {
			return unknown("the host's pull agent last synced %q, whose %s differs from main's head %s", last, changed, snap.head), nil
		}
	}
	name := db.container(t)
	c, ok := report.database(name, t.app)
	if !ok {
		return unknown("the host synced main's head %s, and its newest node report shows no %s", snap.head, name), nil
	}
	unhealthy, waits := c.DatabaseHealth(r.Desired.NeverActive, db.Engine.Trouble)
	if waits != "" {
		return unknown("the host synced main's head %s, and %s", snap.head, waits), nil
	}
	runs := fmt.Sprintf("%s runs %s, %s", c.Name, c.Digest, c.Health)
	if c.Read() == ContainerNotRunning {
		runs = c.Name + " reads " + c.State
	}
	fixed := db.fixedValues(t)
	obs := contract.Observation{
		State: contract.Present, Identity: db.identity(repo, t),
		Outputs:      map[string]string{"dbName": fixed["dbName"], "host": fixed["host"], "port": fixed["port"], "username": fixed["username"]},
		StatusDetail: fmt.Sprintf("the host synced %s's database as main's head %s holds it, and %s", t.app, snap.head, runs),
		Unhealthy:    unhealthy,
	}
	return obs, nil
}

// DatabaseHealth judges a database's container: why the answer waits while it starts, or while it is down or unhealthy
// before the database ever read active, quoting the last line trouble matches, and otherwise its Unhealthy.
func (c Container) DatabaseHealth(neverActive bool, trouble *regexp.Regexp) (*contract.Unhealthy, string) {
	reading, quote := c.Read(), c.quote(trouble)
	switch {
	case reading == ContainerStarting && c.State == stateRunning:
		return nil, c.Name + " is still starting" + quote
	case reading == ContainerStarting, reading == ContainerNotRunning && neverActive:
		return nil, c.Name + " reads " + c.State + quote
	case reading == ContainerUnhealthy && neverActive:
		return nil, c.Name + " reads unhealthy before it ever read active" + quote
	}
	return c.Unhealthy(ContainerCauses{NotRunning: DatabaseNotRunning, Unhealthy: DatabaseUnhealthy}), ""
}

// changedSince names the first of the database's files that differs between the commit the agent synced and main's
// head, "" when none does, since a commit elsewhere in the repository leaves the database the host runs current.
func (db Database) changedSince(ctx context.Context, repo *hostRepo, app, synced, head string) (string, error) {
	for _, name := range db.landedFiles() {
		then, heldThen, err := repo.client.ReadFileAt(ctx, repo.fullName, synced, appPath(app, name))
		if err != nil {
			return "", err
		}
		now, heldNow, err := repo.client.ReadFileAt(ctx, repo.fullName, head, appPath(app, name))
		if err != nil {
			return "", err
		}
		if heldThen != heldNow || !bytes.Equal(then, now) {
			return name, nil
		}
	}
	return "", nil
}

func unknown(format string, args ...any) contract.Observation {
	return contract.Observation{State: contract.Unknown, StatusDetail: fmt.Sprintf(format, args...)}
}
