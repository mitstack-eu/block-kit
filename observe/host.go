package observe

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
)

// HostCommander runs a shell script on an EC2 instance and returns its
// standard output.
type HostCommander interface {
	RunOnInstance(ctx context.Context, d contract.Desired, instanceID, script string) (stdout string, err error)
}

// A name is interpolated into a script, so only names that need no quoting
// are run.
var (
	containerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	databaseName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`)
)

// EC2Container observes an aws.ec2-container by asking the instance its
// identity names for the state of the container it names: running is
// present, no such container is absent, and any other state is unknown.
type EC2Container struct {
	API HostCommander
}

// ObserveResource implements handler.Observer.
func (o EC2Container) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	ids, obs, ok := identityKeys(r, "instanceId", "container")
	if !ok {
		return obs, nil
	}
	if !containerName.MatchString(ids[1]) {
		return unknown("resource %q: container name %q is not one the observer can ask about", r.Name, ids[1]), nil
	}
	script := fmt.Sprintf("docker inspect --format '{{.State.Status}}' %s 2>/dev/null || true", ids[1])
	stdout, err := o.API.RunOnInstance(ctx, r.Desired, ids[0], script)
	if err != nil {
		return contract.Observation{}, err
	}
	switch state := strings.TrimSpace(stdout); state {
	case "running":
		return present(nil), nil
	case "":
		return absent(), nil
	default:
		return unknown("resource %q: container %s is %s", r.Name, ids[1], state), nil
	}
}

// EC2PostgresDatabase observes an aws.ec2-postgres-database by asking the
// Postgres server on the instance its identity names whether the database
// it names exists, reporting its name as dbName.
type EC2PostgresDatabase struct {
	API HostCommander
}

// ObserveResource implements handler.Observer.
func (o EC2PostgresDatabase) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	ids, obs, ok := identityKeys(r, "instanceId", "database")
	if !ok {
		return obs, nil
	}
	if !databaseName.MatchString(ids[1]) {
		return unknown("resource %q: database name %q is not one the observer can ask about", r.Name, ids[1]), nil
	}
	script := fmt.Sprintf(`sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname = '%s'"`, ids[1])
	stdout, err := o.API.RunOnInstance(ctx, r.Desired, ids[0], script)
	if err != nil {
		return contract.Observation{}, err
	}
	switch answer := strings.TrimSpace(stdout); answer {
	case "1":
		return present(map[string]string{"dbName": ids[1]}), nil
	case "":
		return absent(), nil
	default:
		return unknown("resource %q: asking for database %s answered %q", r.Name, ids[1], answer), nil
	}
}
