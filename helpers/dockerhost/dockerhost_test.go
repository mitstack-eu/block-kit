package dockerhost_test

import (
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
)

const publicIP = "203.0.113.10"

var shop = dockerhost.Route{Hostname: "shop.apps.example.com", Upstream: "shop-app:8080"}

func TestCaddyfile_RoutesEachHostnameAndServesTheIPOnTheInternalCA(t *testing.T) {
	blog := []dockerhost.Route{
		{Hostname: "blog.example.org", Upstream: "blog-app:3000"},
		{Hostname: "www.blog.example.org", Upstream: "blog-app:3000"},
	}
	for _, tc := range []struct {
		golden string
		routes []dockerhost.Route
	}{
		{"no-route", nil},
		{"one-route", []dockerhost.Route{shop}},
		{"several-routes", append([]dockerhost.Route{shop}, blog...)},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			edge := dockerhost.Edge{PublicIP: publicIP, IPUpstream: shop.Upstream, Routes: tc.routes}
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden+".Caddyfile"))
			if err != nil {
				t.Fatalf("reading the golden Caddyfile: %v", err)
			}

			file, err := edge.Caddyfile()

			if err != nil {
				t.Fatalf("Caddyfile(%+v): %v", edge, err)
			}
			if file.Path != "/opt/app/Caddyfile" || file.Mode != "0644" {
				t.Errorf("Caddyfile is written to %s with mode %s; want /opt/app/Caddyfile with mode 0644", file.Path, file.Mode)
			}
			if file.Content != string(want) {
				t.Errorf("Caddyfile(%+v) =\n%s\nwant testdata/%s.Caddyfile:\n%s", edge, file.Content, tc.golden, want)
			}
		})
	}
}

func TestCaddyfile_RefusesAValueThatCouldBreakOutOfTheCaddyfile(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*dockerhost.Edge)
	}{
		{"an IPv6 address", func(e *dockerhost.Edge) { e.PublicIP = "2001:db8::1" }},
		{"an IPv4-mapped IPv6 address", func(e *dockerhost.Edge) { e.PublicIP = "::ffff:" + publicIP }},
		{"an address followed by a brace", func(e *dockerhost.Edge) { e.PublicIP = publicIP + " {" }},
		{"a hostname that is an IP address", func(e *dockerhost.Edge) { e.Routes[0].Hostname = publicIP }},
		{"a hostname followed by a brace", func(e *dockerhost.Edge) { e.Routes[0].Hostname = "shop.example.com {" }},
		{"a hostname carrying a second line", func(e *dockerhost.Edge) { e.Routes[0].Hostname = "shop.example.com\nevil.example.com" }},
		{"a hostname of one label", func(e *dockerhost.Edge) { e.Routes[0].Hostname = "localhost" }},
		{"the same hostname routed twice", func(e *dockerhost.Edge) { e.Routes = append(e.Routes, shop) }},
		{"an upstream with no port", func(e *dockerhost.Edge) { e.Routes[0].Upstream = "shop-app" }},
		{"an upstream on port 0", func(e *dockerhost.Edge) { e.Routes[0].Upstream = "shop-app:0" }},
		{"an upstream past port 65535", func(e *dockerhost.Edge) { e.Routes[0].Upstream = "shop-app:65536" }},
		{"an IP upstream carrying a directive", func(e *dockerhost.Edge) { e.IPUpstream = "shop-app:8080\n  respond 200" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edge := dockerhost.Edge{PublicIP: publicIP, IPUpstream: shop.Upstream, Routes: []dockerhost.Route{shop}}
			tc.mutate(&edge)

			file, err := edge.Caddyfile()

			if err == nil {
				t.Errorf("Caddyfile(%+v) = %q with no error; want a refusal", edge, file.Content)
			}
		})
	}
}

func TestSSHLockdown_TurnsOffPasswordAndRootLoginInADropInSortedBeforeCloudInits(t *testing.T) {
	file := dockerhost.SSHLockdown()

	dir, base := path.Split(file.Path)
	if dir != "/etc/ssh/sshd_config.d/" || base >= "50-cloud-init.conf" || file.Mode != "0644" {
		t.Errorf("the drop-in is %s with mode %s; want mode 0644 in /etc/ssh/sshd_config.d/, sorted before 50-cloud-init.conf, since sshd keeps the first value it reads", file.Path, file.Mode)
	}
	if want := "PasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\n"; file.Content != want {
		t.Errorf("the drop-in reads %q; want %q", file.Content, want)
	}
}

const edgeRun = "docker run -d --name caddy --restart=always --network appnet -p 80:80 -p 443:443 " +
	"-v /opt/app/Caddyfile:/etc/caddy/Caddyfile:ro -v caddy_data:/data -v caddy_config:/config caddy:2"

func TestSetup_InstallsDockerAndStartsTheEdgeRetryingEachTenTimesAtMost(t *testing.T) {
	for _, tc := range []struct {
		name                                                             string
		installFailures, pullFailures, wantExit, wantInstalls, wantPulls int
	}{
		{"docker installs on the third try and the edge image pulls on the second", 2, 1, 0, 3, 2},
		{"docker never installs", 10, 0, 1, 10, 0},
		{"the edge image never pulls", 0, 10, 1, 1, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, exit := runSetup(t, tc.installFailures, tc.pullFailures)

			installs, pulls := count(calls, "apt-get install -y docker.io"), count(calls, "docker pull caddy:2")
			if exit != tc.wantExit || installs != tc.wantInstalls || pulls != tc.wantPulls {
				t.Errorf("setup exited %d after %d installs and %d pulls; want %d, %d and %d\ncalls:\n%s",
					exit, installs, pulls, tc.wantExit, tc.wantInstalls, tc.wantPulls, strings.Join(calls, "\n"))
			}
			started := count(calls, "systemctl enable --now docker") == 1 && count(calls, "docker network create appnet") == 1 && count(calls, edgeRun) == 1
			if tc.wantExit == 0 && !started {
				t.Errorf("a setup that succeeded did not enable docker, create appnet and run the edge once each:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

// runSetup runs the setup script with only stubs on PATH: apt-get fails installFailures times before it puts docker
// on PATH, and docker pull fails pullFailures times.
func runSetup(t *testing.T, installFailures, pullFailures int) (calls []string, exit int) {
	t.Helper()
	setup := dockerhost.Setup()
	if setup.Path != "/usr/local/sbin/mitstack-host-setup" || setup.Mode != "0755" {
		t.Fatalf("the setup script is %s with mode %s; want /usr/local/sbin/mitstack-host-setup with mode 0755", setup.Path, setup.Mode)
	}
	state, bin := t.TempDir(), t.TempDir()
	linkTools(t, bin, "seq", "grep", "mv")
	writeStub(t, bin, "sleep", "")
	writeStub(t, bin, "systemctl", "")
	writeStub(t, bin, "apt-get", `[ "$1" = install ] || exit 0
[ "$(grep -c '^apt-get install' "$STATE/calls")" -gt "$INSTALL_FAILURES" ] || exit 100
mv "$STATE/docker" "$BIN/docker"
`)
	writeStub(t, state, "docker", `case "$1" in
network) [ "$2" = create ] ;;
pull) [ "$(grep -c '^docker pull' "$STATE/calls")" -gt "$PULL_FAILURES" ] ;;
esac
`)
	script := filepath.Join(state, "setup")
	if err := os.WriteFile(script, []byte(setup.Content), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", script)
	cmd.Env = []string{"PATH=" + bin, "BIN=" + bin, "STATE=" + state,
		"INSTALL_FAILURES=" + strconv.Itoa(installFailures), "PULL_FAILURES=" + strconv.Itoa(pullFailures)}
	out, err := cmd.CombinedOutput()

	if exitErr := (*exec.ExitError)(nil); err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("running the setup script: %v\n%s", err, out)
	}
	logged, _ := os.ReadFile(filepath.Join(state, "calls"))
	return strings.Split(strings.TrimSpace(string(logged)), "\n"), cmd.ProcessState.ExitCode()
}

func linkTools(t *testing.T, bin string, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("finding %s: %v", tool, err)
		}
		if err := os.Symlink(real, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
}

// writeStub writes an executable that logs its own call to $STATE/calls and then runs body.
func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	script := "#!/bin/sh\necho \"" + name + " $*\" >> \"$STATE/calls\"\n" + body
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func count(calls []string, call string) int {
	return len(slices.DeleteFunc(slices.Clone(calls), func(c string) bool { return c != call }))
}
