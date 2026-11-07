package observe

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
)

// A script that exits nonzero is an error, never output.
type HostCommander interface {
	RunOnInstance(ctx context.Context, d contract.Desired, instanceID, script string) (stdout string, err error)
}

// A name is interpolated into a script, so only names that need no quoting are run.
var containerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// The probe prints absent only when Docker answers that no such container exists.
const containerProbe = `out=$(docker inspect --format '{{.State.Status}}' %[1]s 2>&1) && { printf '%%s\n' "$out"; exit 0; }
case "$out" in *"No such object"*|*"No such container"*) echo absent ;; *) printf '%%s\n' "$out" >&2; exit 1 ;; esac`

// The label probe prints the names of the containers, stopped ones included, that carry the subscription's label.
const labelProbe = `docker ps -a --filter 'label=%s=%s' --format '{{.Names}}'`

// EC2Container observes an aws.ec2-container by its instanceId and container, or with neither while unsubscribing by the label ContainerSubscriptionLabel on the bound instance's containers.
type EC2Container struct {
	API HostCommander
}

// boundInstance is the instance the subscription's aws.ec2-instance requirement binds, empty when it binds none or several.
func boundInstance(d contract.Desired) string {
	var ids []string
	for ref, typ := range d.RequirementTypes {
		if id := d.Bindings[ref]["instanceId"]; typ == "aws.ec2-instance" && id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 {
		return ""
	}
	return ids[0]
}

func (o EC2Container) heldByLabel(ctx context.Context, r contract.ResourceRef, noKey contract.Observation) (contract.Observation, error) {
	instance, sub := r.Identity["instanceId"], r.Desired.SubscriptionID
	if instance == "" {
		instance = boundInstance(r.Desired)
	}
	if !r.Unsubscribing || instance == "" || !containerName.MatchString(sub) {
		return noKey, nil
	}
	stdout, err := o.API.RunOnInstance(ctx, r.Desired, instance, fmt.Sprintf(labelProbe, ContainerSubscriptionLabel, sub))
	if err != nil {
		return contract.Observation{}, err
	}
	names := strings.Fields(stdout)
	if len(names) == 0 {
		return absent(), nil
	}
	return contract.Observation{State: contract.Present, Identity: map[string]string{"instanceId": instance, "container": slices.Min(names)}}, nil
}

func (o EC2Container) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	ids, obs, ok := identityKeys(r, "instanceId", "container")
	if !ok {
		return o.heldByLabel(ctx, r, obs)
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
		return present(r, nil), nil
	case "absent":
		return absent(), nil
	case "":
		return unknown("resource %q: the probe of container %s printed nothing", r.Name, ids[1]), nil
	default:
		return unknown("resource %q: container %s is %s", r.Name, ids[1], state), nil
	}
}
