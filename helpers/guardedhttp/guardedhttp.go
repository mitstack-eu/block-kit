// Package guardedhttp builds the HTTP client a capability operator calls its vendor with: https only, no redirect followed, only the declared hosts, and no loopback, private, link-local or unspecified address.
package guardedhttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ErrRefused is wrapped by every error the client returns for a request it will not send, so a caller tells a guard from a transport failure.
var ErrRefused = errors.New("guardedhttp: refused")

const defaultTimeout = 30 * time.Second

// Config names the hosts the client may reach, and its Resolve, Dial and RootCAs let a test stand a server in place of the vendor without lifting the address checks.
type Config struct {
	// Hosts are the exact host names, or address literals, the operator declares; a request to any other host is refused before the host is resolved.
	Hosts []string
	// Timeout bounds one request, 30 seconds when zero.
	Timeout time.Duration

	Resolve func(ctx context.Context, host string) ([]net.IP, error)
	Dial    func(ctx context.Context, network, address string) (net.Conn, error)
	RootCAs *x509.CertPool
}

// New returns a client whose every request is checked before a connection opens, and whose connection dials an address it has just checked rather than the name.
func New(cfg Config) (*http.Client, error) {
	hosts, err := declaredHosts(cfg.Hosts)
	if err != nil {
		return nil, err
	}
	g := &guard{hosts: hosts, resolve: cfg.Resolve, dial: cfg.Dial}
	if g.resolve == nil {
		g.resolve = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	if g.dial == nil {
		g.dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	g.next = &http.Transport{
		Proxy:               nil,
		DialContext:         g.dialChecked,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: cfg.RootCAs},
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{Transport: g, Timeout: timeout, CheckRedirect: refuseRedirect}, nil
}

func declaredHosts(hosts []string) (map[string]bool, error) {
	if len(hosts) == 0 {
		return nil, errors.New("guardedhttp: Config.Hosts lists no host, so the client could reach nothing")
	}
	declared := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		name := normalizeHost(h)
		if name == "" || strings.ContainsAny(name, "/:@*?# ") && !isAddressLiteral(name) {
			return nil, fmt.Errorf("guardedhttp: %q is not a host name; list the bare host, with no scheme, port, path or wildcard", h)
		}
		declared[name] = true
	}
	return declared, nil
}

func isAddressLiteral(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return fmt.Errorf("%w: a redirect is never followed, since the guards check only the first address", ErrRefused)
}

type guard struct {
	hosts   map[string]bool
	resolve func(ctx context.Context, host string) ([]net.IP, error)
	dial    func(ctx context.Context, network, address string) (net.Conn, error)
	next    *http.Transport
}

// RoundTrip refuses a request before the transport resolves or dials anything, and its errors name no URL, since one can carry a credential.
func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	switch {
	case req.URL.Scheme != "https":
		return nil, fmt.Errorf("%w: the request is not https", ErrRefused)
	case req.URL.User != nil:
		return nil, fmt.Errorf("%w: the request carries credentials in its URL", ErrRefused)
	case !g.hosts[normalizeHost(req.URL.Hostname())]:
		return nil, fmt.Errorf("%w: the request's host is not one the operator declares", ErrRefused)
	}
	return g.next.RoundTrip(req)
}

// dialChecked dials the address it checked, never the name, so a second lookup cannot answer differently.
func (g *guard) dialChecked(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: the address is malformed", ErrRefused)
	}
	ips, err := g.addresses(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		conn, err := g.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

func (g *guard) addresses(ctx context.Context, host string) ([]net.IP, error) {
	var ips []net.IP
	if addr, err := netip.ParseAddr(host); err == nil {
		ips = []net.IP{net.IP(addr.Unmap().AsSlice())}
	} else {
		resolved, err := g.resolve(ctx, host)
		if err != nil || len(resolved) == 0 {
			return nil, fmt.Errorf("%w: the host did not resolve", ErrRefused)
		}
		ips = resolved
	}
	for _, ip := range ips {
		if refusedAddress(ip) {
			return nil, fmt.Errorf("%w: the host is at a loopback, private, link-local or unspecified address", ErrRefused)
		}
	}
	return ips, nil
}

func refusedAddress(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
