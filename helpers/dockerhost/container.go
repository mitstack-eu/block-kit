package dockerhost

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
)

// Container is one container a Docker host's node report lists under an app; Logs is nil from a host release that sends no output.
type Container struct {
	Name         string  `json:"container"`
	Subscription string  `json:"subscription"`
	Image        string  `json:"image"`
	Digest       string  `json:"digest"`
	State        string  `json:"state"`
	Health       string  `json:"health"`
	Logs         *string `json:"logs"`
}

// ContainerReading is the one answer every reader returns for a container; ContainerStarting reads neither healthy nor failed.
type ContainerReading int

const (
	ContainerStarting ContainerReading = iota
	ContainerHealthy
	ContainerNotRunning
	ContainerUnhealthy
)

var notRunning = []string{"restarting", "exited", "dead", "paused"}

const (
	stateRunning    = "running"
	healthStarting  = "starting"
	healthUnhealthy = "unhealthy"
)

func (r ContainerReading) Failed() bool { return r == ContainerNotRunning || r == ContainerUnhealthy }

// Read judges the container by its state first and then its health, reading a container with no health check by its state alone.
func (c Container) Read() ContainerReading {
	switch {
	case slices.Contains(notRunning, c.State):
		return ContainerNotRunning
	case c.State != stateRunning || c.Health == healthStarting:
		return ContainerStarting
	case c.Health == healthUnhealthy:
		return ContainerUnhealthy
	}
	return ContainerHealthy
}

// ContainerCauses are the causes a resource type lists, word for word, for its container not running and reading unhealthy.
type ContainerCauses struct {
	NotRunning, Unhealthy string
}

// Unhealthy quotes no output, so one failure reads the same at every restart and is re-driven once.
func (c Container) Unhealthy(causes ContainerCauses) *contract.Unhealthy {
	switch c.Read() {
	case ContainerNotRunning:
		return &contract.Unhealthy{Cause: causes.NotRunning, Detail: c.Name + " reads " + c.State}
	case ContainerUnhealthy:
		return &contract.Unhealthy{Cause: causes.Unhealthy, Detail: c.Name + " reads " + healthUnhealthy}
	}
	return nil
}

func (c Container) quote(trouble *regexp.Regexp) string {
	if c.Logs == nil {
		return "; this host's node report carries no container output, so the cause cannot be read until the host runs a release that sends it"
	}
	if trouble == nil {
		return ""
	}
	for _, line := range slices.Backward(strings.Split(*c.Logs, "\n")) {
		if trouble.MatchString(line) {
			return ": " + line
		}
	}
	return ""
}

var replacedID = regexp.MustCompile(`^[0-9a-f]{12}$`)

// Replacement finds the container Compose created, named by c's id prefix and c's name, before it stopped c to replace it.
func (c Container) Replacement(listed []Container) (Container, bool) {
	for _, o := range listed {
		if id, found := strings.CutSuffix(o.Name, "_"+c.Name); found && replacedID.MatchString(id) {
			return o, true
		}
	}
	return Container{}, false
}

func (c Container) replacedBy(o Container, what string) string {
	return c.Name + " is being replaced by " + o.Name + ", so whether " + what + " serves cannot be read until the replacement starts"
}

// AppHealth answers why it waits while a container starts or is replaced, so the verdict before it stands.
func (r Report) AppHealth(app, sub string, causes ContainerCauses) (*contract.Unhealthy, string, error) {
	containers, err := r.AppContainers(app)
	if err != nil {
		return nil, "", err
	}
	waits := ""
	for _, c := range containers {
		if c.Subscription != sub {
			continue
		}
		o, replaced := c.Replacement(containers)
		switch reading := c.Read(); {
		case replaced:
			waits = c.replacedBy(o, "the app")
		case reading.Failed():
			return c.Unhealthy(causes), "", nil
		case reading == ContainerStarting && waits == "":
			waits = c.Name + " is still starting, so whether it serves cannot be read until its health check passes"
		}
	}
	return nil, waits, nil
}

// AppContainers lists the containers the report holds under app, and fails the whole list when one entry cannot be read.
func (r Report) AppContainers(app string) ([]Container, error) {
	var listed struct {
		Host struct {
			Apps map[string][]Container `json:"apps"`
		} `json:"host"`
	}
	if err := r.Decode(&listed); err != nil {
		return nil, fmt.Errorf("decoding app %s's containers from the host's newest node report: %w", app, err)
	}
	return listed.Host.Apps[app], nil
}
