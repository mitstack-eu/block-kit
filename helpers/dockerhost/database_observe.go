package dockerhost

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

const (
	healthUnhealthy = "unhealthy"
	// DatabaseUnhealthy is the cause an unhealthy database answers, which its catalogue type lists word for word.
	DatabaseUnhealthy = "the database's container reads unhealthy on the host"
)

// databaseReport is the host's newest node report, with the parts only a Database reads.
type databaseReport struct {
	Report
	Host struct {
		Apps    map[string][]databaseContainer `json:"apps"`
		Backups struct {
			Error *string `json:"error"`
		} `json:"backups"`
	} `json:"host"`
}

type databaseContainer struct {
	Container string  `json:"container"`
	Digest    string  `json:"digest"`
	State     string  `json:"state"`
	Health    string  `json:"health"`
	Logs      *string `json:"logs"`
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

func (h databaseReport) database(name string, app string) (databaseContainer, bool) {
	for _, c := range h.Host.Apps[app] {
		if c.Container == name {
			return c, true
		}
	}
	return databaseContainer{}, false
}

// trouble quotes the last line of the container's output that names a failure, or says the host's release sends none.
func (db Database) trouble(c databaseContainer) string {
	if c.Logs == nil {
		return "; this host's node report carries no container output, so the cause cannot be read until the host runs a release that sends it"
	}
	if db.Engine.Trouble == nil {
		return ""
	}
	lines := strings.Split(*c.Logs, "\n")
	for _, line := range slices.Backward(lines) {
		if db.Engine.Trouble.MatchString(line) {
			return ": " + line
		}
	}
	return ""
}

// ObserveResource reads the database's files at main's head, and reads them present only once the host's newest node
// report shows its pull agent synced a commit holding those same files and the database's container running.
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
	unhealthy := c.Health == healthUnhealthy
	switch {
	case c.State != "running":
		return unknown("the host synced main's head %s, and %s reads %s%s", snap.head, name, c.State, db.trouble(c)), nil
	case c.Health == "starting":
		return unknown("the host synced main's head %s, and %s is still starting%s", snap.head, name, db.trouble(c)), nil
	case unhealthy && r.Desired.NeverActive:
		return unknown("the host synced main's head %s, and %s reads unhealthy before it ever read active%s", snap.head, name, db.trouble(c)), nil
	}
	fixed := db.fixedValues(t)
	obs := contract.Observation{
		State: contract.Present, Identity: db.identity(repo, t),
		Outputs:      map[string]string{"dbName": fixed["dbName"], "host": fixed["host"], "port": fixed["port"], "username": fixed["username"]},
		StatusDetail: fmt.Sprintf("the host synced %s's database as main's head %s holds it, and %s runs %s, %s", t.app, snap.head, c.Container, c.Digest, c.Health),
	}
	if unhealthy {
		obs.Unhealthy = &contract.Unhealthy{Cause: DatabaseUnhealthy, Detail: name + " reads unhealthy" + db.trouble(c)}
	}
	return obs, nil
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
