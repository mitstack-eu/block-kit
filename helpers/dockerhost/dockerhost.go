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
	caddyfileMount    = "-v " + caddyfilePath + ":/etc/caddy/Caddyfile:ro"
	edgeRouteDir      = "/etc/caddy/routes"
	compression       = "encode zstd gzip"
	setupPath        = "/usr/local/sbin/mitstack-host-setup"
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

// Edge serves each route's hostname on a publicly trusted certificate, and PublicIP on caddy's internal CA to
// IPUpstream, or with a 204 when IPUpstream is empty. Compress writes encode in every site, and RouteDir is a host
// directory of more sites, one .caddy file each, so routes change after boot without a new Caddyfile.
type Edge struct {
	PublicIP   string
	IPUpstream string
	Routes     []Route
	Compress   bool
	RouteDir   string
}

var (
	routeDirRe = regexp.MustCompile(`^(/[A-Za-z0-9_][A-Za-z0-9._-]*)+$`)
	hostnameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)
	upstreamRe = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?:([0-9]{1,5})$`)
)

func Setup() File {
	return Edge{}.Setup()
}

// Setup is the package's Setup with the edge's RouteDir mounted read-only where its Caddyfile imports it.
func (e Edge) Setup() File {
	script := setupScript
	if e.RouteDir != "" {
		script = strings.Replace(script, caddyfileMount, caddyfileMount+" -v "+e.RouteDir+":"+edgeRouteDir+":ro", 1)
	}
	return File{Path: setupPath, Mode: executableMode, Content: script}
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
	blocks := []string{block("{", "auto_https disable_redirects", "default_sni "+e.PublicIP)}
	for _, r := range e.Routes {
		blocks = append(blocks, e.site(r))
	}
	if e.RouteDir != "" {
		blocks = append(blocks, "import "+edgeRouteDir+"/*.caddy\n")
	}
	ipSite := append([]string{"tls internal"}, e.encoding()...)
	if e.IPUpstream == "" {
		ipSite = append(ipSite, "respond 204")
	} else {
		ipSite = append(ipSite, "reverse_proxy "+e.IPUpstream)
	}
	blocks = append(blocks,
		block("https://"+e.PublicIP+" {", ipSite...),
		block(":80 {", "redir https://{host}{uri} permanent"),
	)
	return File{Path: caddyfilePath, Mode: readableMode, Content: strings.Join(blocks, "\n")}, nil
}

// Site is one route's site as a file in RouteDir holds it, refusing a route Caddyfile would refuse.
func (e Edge) Site(r Route) (string, error) {
	if err := checkRoute(r); err != nil {
		return "", err
	}
	return e.site(r), nil
}

func (e Edge) site(r Route) string {
	return block(r.Hostname+" {", append(e.encoding(), "reverse_proxy "+r.Upstream)...)
}

func (e Edge) encoding() []string {
	if e.Compress {
		return []string{compression}
	}
	return nil
}

func block(opening string, directives ...string) string {
	return opening + "\n  " + strings.Join(directives, "\n  ") + "\n}\n"
}

func (e Edge) check() error {
	if err := checkPublicIP(e.PublicIP); err != nil {
		return err
	}
	if e.IPUpstream != "" {
		if err := checkUpstream(e.IPUpstream); err != nil {
			return err
		}
	}
	if e.RouteDir != "" && !routeDirRe.MatchString(e.RouteDir) {
		return fmt.Errorf("edge: route directory %q is not an absolute path of plain names", e.RouteDir)
	}
	return checkRoutes(e.Routes)
}

func checkPublicIP(address string) error {
	// net.ParseIP reads an IPv4-mapped IPv6 address such as ::ffff:203.0.113.10 as IPv4 too.
	if ip := net.ParseIP(address); ip == nil || ip.To4() == nil || strings.Contains(address, ":") {
		return fmt.Errorf("edge: %q is not an IPv4 address", address)
	}
	return nil
}

func checkRoutes(routes []Route) error {
	routed := map[string]bool{}
	for _, r := range routes {
		if err := checkRoute(r); err != nil {
			return err
		}
		if routed[r.Hostname] {
			return fmt.Errorf("edge: %s is routed twice, and caddy refuses a config that defines a site twice", r.Hostname)
		}
		routed[r.Hostname] = true
	}
	return nil
}

func checkRoute(r Route) error {
	if !hostnameRe.MatchString(r.Hostname) {
		return fmt.Errorf("edge: %q is not a lowercase hostname of two or more labels", r.Hostname)
	}
	if net.ParseIP(r.Hostname) != nil {
		return fmt.Errorf("edge: route %s is an IP address, which the edge serves on its internal CA already", r.Hostname)
	}
	return checkUpstream(r.Upstream)
}

func checkUpstream(upstream string) error {
	match := upstreamRe.FindStringSubmatch(upstream)
	if match == nil {
		return fmt.Errorf("edge: upstream %q is not a container name and port, as name:port", upstream)
	}
	if port, _ := strconv.Atoi(match[1]); port < 1 || port > highestPortNumber {
		return fmt.Errorf("edge: upstream %q names port %d, outside 1 to %d", upstream, port, highestPortNumber)
	}
	return nil
}
