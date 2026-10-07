package observe

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
)

// HostCommander runs a shell script on an EC2 instance and returns its
// standard output. A script that exits nonzero is an error, never output.
type HostCommander interface {
	RunOnInstance(ctx context.Context, d contract.Desired, instanceID, script string) (stdout string, err error)
}

// A name is interpolated into a script, so only names that need no quoting
// are run.
var (
	containerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	databaseName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`)
)

// The probe prints absent only when Docker answers that no such container
// exists; any other failure exits nonzero.
const containerProbe = `out=$(docker inspect --format '{{.State.Status}}' %[1]s 2>&1) && { printf '%%s\n' "$out"; exit 0; }
case "$out" in *"No such object"*|*"No such container"*) echo absent ;; *) printf '%%s\n' "$out" >&2; exit 1 ;; esac`

// EC2Container observes an aws.ec2-container through the instance it runs
// on: running is present, absent is absent, anything else is unknown, and
// a failed probe is an error.
type EC2Container struct {
	API HostCommander
}

func (o EC2Container) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	ids, obs, ok := identityKeys(r, "instanceId", "container")
	if !ok {
		return obs, nil
	}
	if !containerName.MatchString(ids[1]) {
		return unknown("resource %q: container name %q is not one the observer can ask about", r.Name, ids[1]), nil
	}
	stdout, err := o.API.RunOnInstance(ctx, r.Desired, ids[0], fmt.Sprintf(containerProbe, ids[1]))
	if err != nil {
		return contract.Observation{}, err
	}
	switch state := strings.TrimSpace(stdout); state {
	case "running":
		return present(nil), nil
	case "absent":
		return absent(), nil
	case "":
		return unknown("resource %q: the probe of container %s printed nothing", r.Name, ids[1]), nil
	default:
		return unknown("resource %q: container %s is %s", r.Name, ids[1], state), nil
	}
}

// databaseProbe prints 1 when the database exists and absent when it does
// not; a failed psql exits nonzero.
const databaseProbe = `out=$(sudo -u postgres psql -v ON_ERROR_STOP=1 -tAc "SELECT 1 FROM pg_database WHERE datname = '%[1]s'") || exit 1
if [ -n "$out" ]; then printf '%%s\n' "$out"; else echo absent; fi`

// EC2PostgresDatabase observes an aws.ec2-postgres-database through the
// instance its server runs on, reporting presence, identity and dbName
// only. A block that provides it reports the type's other required outputs
// from its own observer.
type EC2PostgresDatabase struct {
	API HostCommander
}

func (o EC2PostgresDatabase) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	ids, obs, ok := identityKeys(r, "instanceId", "database")
	if !ok {
		return obs, nil
	}
	if !databaseName.MatchString(ids[1]) {
		return unknown("resource %q: database name %q is not one the observer can ask about", r.Name, ids[1]), nil
	}
	stdout, err := o.API.RunOnInstance(ctx, r.Desired, ids[0], fmt.Sprintf(databaseProbe, ids[1]))
	if err != nil {
		return contract.Observation{}, err
	}
	switch answer := strings.TrimSpace(stdout); answer {
	case "1":
		return present(map[string]string{"dbName": ids[1]}), nil
	case "absent":
		return absent(), nil
	case "":
		return unknown("resource %q: the probe of database %s printed nothing", r.Name, ids[1]), nil
	default:
		return unknown("resource %q: asking for database %s answered %q", r.Name, ids[1], answer), nil
	}
}
