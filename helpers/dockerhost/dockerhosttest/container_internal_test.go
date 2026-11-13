package dockerhosttest

import (
	"slices"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
)

// A rendered service is created as the host runs it: its limits, its own data volume, its health check, the given
// environment and network, and its entrypoint with Compose's "$$" read back as "$".
func TestCreateArgs_RunTheServiceAsTheHostWouldOnItsOwnVolumeWithTheEnvironmentAndNetworkGiven(t *testing.T) {
	s := dockerhost.ComposeService{
		Image: "example.org/store:2.1@sha256:0f9e", CPUs: "0.5", MemLimit: 536870912, MemswapLimit: 536870912,
		Entrypoint:  []string{"sh", "-c", `exec store --password="$$DB_PASSWORD"`, "store"},
		Command:     []string{"serve"},
		Volumes:     []string{"store-data:/data"},
		SecurityOpt: []string{"no-new-privileges:true"},
		Healthcheck: dockerhost.ComposeHealth{Test: []string{"CMD", "store", "ping"}},
	}

	got := createArgs("scratch-db", s, "scratch-net", []string{"DB_PASSWORD=pw$1"})

	want := []string{
		"create", "--name", "scratch-db", "--cpus", "0.5", "--memory", "536870912", "--memory-swap", "536870912",
		"--volume", "scratch-db:/data", "--security-opt", "no-new-privileges:true",
		"--health-cmd", "store ping", "--health-interval", "1s", "--env", "DB_PASSWORD=pw$1", "--network", "scratch-net",
		"--entrypoint", "sh", "example.org/store:2.1@sha256:0f9e", "-c", `exec store --password="$DB_PASSWORD"`, "store", "serve",
	}
	if !slices.Equal(got, want) {
		t.Errorf("createArgs =\n%q\nwant\n%q", got, want)
	}
	if got := createArgs("scratch-db", s, "", nil); slices.Contains(got, "--network") {
		t.Errorf("createArgs with no network = %q; want the default network", got)
	}
}
