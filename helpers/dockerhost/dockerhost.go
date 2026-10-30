// Package dockerhost renders the setup of a host that runs apps as containers: Ubuntu's Docker alone, the appnet
// network, a caddy edge container routing each hostname to an app, and sshd locked to key login.
package dockerhost

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

const Package = "docker.io"

const EdgeRunning = `[ "$(docker container inspect -f '{{.State.Running}}' caddy)" = true ]`

const (
	caddyfilePath     = "/opt/app/Caddyfile"
	setupPath         = "/usr/local/sbin/mitstack-host-setup"
	sshDropInPath     = "/etc/ssh/sshd_config.d/10-mitstack.conf"
	readableMode      = "0644"
	executableMode    = "0755"
	highestPortNumber = 65535
)

// setupScript retries the Docker install and the edge start, ten attempts each, so one failed package stage or
// registry hiccup at boot does not leave the host without them, and exits non-zero when either never succeeds.
const setupScript = `#!/bin/sh
set -u
for attempt in $(seq 1 10); do
  if command -v docker >/dev/null 2>&1; then
    break
  fi
  apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io
  sleep 10
done
command -v docker >/dev/null 2>&1 || exit 1
systemctl enable --now docker
docker network inspect appnet >/dev/null 2>&1 || docker network create appnet
for attempt in $(seq 1 10); do
  docker rm -f caddy >/dev/null 2>&1
  if docker pull caddy:2 &&
    docker run -d --name caddy --restart=always --network appnet -p 80:80 -p 443:443 -v /opt/app/Caddyfile:/etc/caddy/Caddyfile:ro -v caddy_data:/data -v caddy_config:/config caddy:2
  then
    exit 0
  fi
  sleep 10
done
exit 1
`

type File struct {
	Path    string
	Mode    string
	Content string
}

// Route sends one hostname's requests to an upstream, a container on appnet named with its port as name:port.
type Route struct {
	Hostname string
	Upstream string
}

// Edge serves each route's hostname on a publicly trusted certificate and PublicIP on caddy's internal CA.
type Edge struct {
	PublicIP   string
	IPUpstream string
	Routes     []Route
}

var (
	hostnameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)
	upstreamRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?:([0-9]{1,5})$`)
)

func Setup() File {
	return File{Path: setupPath, Mode: executableMode, Content: setupScript}
}

// SSHLockdown is an sshd drop-in that turns off password, keyboard-interactive and root login. sshd keeps the first
// value it reads for a keyword and reads drop-ins in lexical order, so this one sorts before cloud-init's own.
func SSHLockdown() File {
	return File{
		Path:    sshDropInPath,
		Mode:    readableMode,
		Content: "PasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\n",
	}
}

// Caddyfile refuses any value that could break out of the config, and gives a client sending no SNI the IP's site.
func (e Edge) Caddyfile() (File, error) {
	if err := e.check(); err != nil {
		return File{}, err
	}
	var b strings.Builder
	b.WriteString("{\n  auto_https disable_redirects\n  default_sni " + e.PublicIP + "\n}\n")
	for _, r := range e.Routes {
		b.WriteString("\n" + r.Hostname + " {\n  reverse_proxy " + r.Upstream + "\n}\n")
	}
	b.WriteString("\nhttps://" + e.PublicIP + " {\n  tls internal\n  reverse_proxy " + e.IPUpstream + "\n}\n")
	b.WriteString("\n:80 {\n  redir https://{host}{uri} permanent\n}\n")
	return File{Path: caddyfilePath, Mode: readableMode, Content: b.String()}, nil
}

func (e Edge) check() error {
	if err := checkPublicIP(e.PublicIP); err != nil {
		return err
	}
	if err := checkUpstream(e.IPUpstream); err != nil {
		return err
	}
	return checkRoutes(e.Routes)
}

func checkPublicIP(address string) error {
	if ip := net.ParseIP(address); ip == nil || ip.To4() == nil || strings.Contains(address, ":") {
		return fmt.Errorf("edge: %q is not an IPv4 address", address)
	}
	return nil
}

func checkRoutes(routes []Route) error {
	routed := map[string]bool{}
	for _, r := range routes {
		if !hostnameRe.MatchString(r.Hostname) {
			return fmt.Errorf("edge: %q is not a lowercase hostname of two or more labels", r.Hostname)
		}
		if net.ParseIP(r.Hostname) != nil {
			return fmt.Errorf("edge: route %s is an IP address, which the edge serves on its internal CA already", r.Hostname)
		}
		if routed[r.Hostname] {
			return fmt.Errorf("edge: %s is routed twice, and caddy refuses a config that defines a site twice", r.Hostname)
		}
		routed[r.Hostname] = true
		if err := checkUpstream(r.Upstream); err != nil {
			return err
		}
	}
	return nil
}

func checkUpstream(upstream string) error {
	m := upstreamRe.FindStringSubmatch(upstream)
	if m == nil {
		return fmt.Errorf("edge: upstream %q is not a container name and port, as name:port", upstream)
	}
	if port, _ := strconv.Atoi(m[2]); port < 1 || port > highestPortNumber {
		return fmt.Errorf("edge: upstream %q names port %d, outside 1 to %d", upstream, port, highestPortNumber)
	}
	return nil
}
