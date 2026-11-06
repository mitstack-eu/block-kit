package kittest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/guardedhttp"
)

// guardedAddresses are the hosts a capability operator's client must refuse to dial even when it declares them, written as they appear in a URL.
var guardedAddresses = []string{"10.0.0.1", "169.254.169.254", "[::1]", "[fe80::1]", "[ff02::1]", "0.0.0.0"}

// CheckGuardedClient returns one problem for each guard the client built by newClient lacks: https only, no redirect followed, declared hosts only, and no loopback, private, link-local or unspecified address, each refused with guardedhttp.ErrRefused before a connection opens.
func CheckGuardedClient(newClient func(hosts []string) *http.Client) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	reached := listenCounting()
	defer reached.close()

	for _, scheme := range []string{"http", "https"} {
		url := fmt.Sprintf("%s://%s/", scheme, reached.addr())
		if err := refused(newClient([]string{reached.host()}), url); err == nil {
			add("a request to %s://127.0.0.1 was not refused", scheme)
		} else if !errors.Is(err, guardedhttp.ErrRefused) {
			add("a request to %s://127.0.0.1 failed, but not with guardedhttp.ErrRefused, so the client may have dialed it: %v", scheme, err)
		}
	}
	if n := reached.accepted(); n != 0 {
		add("the client opened %d connection(s) to 127.0.0.1, want none", n)
	}
	for _, host := range guardedAddresses {
		url := "https://" + host + "/"
		err := refused(newClient([]string{trimBrackets(host)}), url)
		if !errors.Is(err, guardedhttp.ErrRefused) {
			add("a request to https://%s was not refused with guardedhttp.ErrRefused: %v", host, err)
		}
	}
	if err := refused(newClient([]string{"declared.example"}), "https://undeclared.example/"); !errors.Is(err, guardedhttp.ErrRefused) {
		add("a request to a host the operator did not declare was not refused with guardedhttp.ErrRefused: %v", err)
	}
	if err := refused(newClient([]string{"declared.example"}), "http://declared.example/"); !errors.Is(err, guardedhttp.ErrRefused) {
		add("a request to a declared host over http was not refused with guardedhttp.ErrRefused: %v", err)
	}
	problems = append(problems, redirectProblems(newClient([]string{"declared.example"}))...)
	return problems
}

// AssertGuardedClient fails tb with each problem CheckGuardedClient returns.
func AssertGuardedClient(tb testing.TB, newClient func(hosts []string) *http.Client) {
	tb.Helper()
	for _, p := range CheckGuardedClient(newClient) {
		tb.Error(p)
	}
}

func redirectProblems(c *http.Client) []string {
	if c == nil {
		return []string{"newClient returned no client"}
	}
	if c.CheckRedirect == nil {
		return []string{"the client has no CheckRedirect, so it follows up to ten redirects, and the guards check only the first URL"}
	}
	req, err := http.NewRequest(http.MethodGet, "https://declared.example/next", nil)
	if err != nil {
		return []string{err.Error()}
	}
	if err := c.CheckRedirect(req, []*http.Request{req}); !errors.Is(err, guardedhttp.ErrRefused) {
		return []string{fmt.Sprintf("the client's CheckRedirect answered %v for a redirect, want guardedhttp.ErrRefused", err)}
	}
	return nil
}

func refused(c *http.Client, url string) error {
	if c == nil {
		return errors.New("newClient returned no client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func trimBrackets(host string) string {
	if len(host) > 1 && host[0] == '[' {
		return host[1 : len(host)-1]
	}
	return host
}

// countingListener accepts connections on a loopback port and counts them without answering.
type countingListener struct {
	l net.Listener
	n atomic.Int64
}

func listenCounting() *countingListener {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return &countingListener{}
	}
	c := &countingListener{l: l}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			c.n.Add(1)
			conn.Close()
		}
	}()
	return c
}

func (c *countingListener) addr() string {
	if c.l == nil {
		return "127.0.0.1:1"
	}
	return c.l.Addr().String()
}

func (c *countingListener) host() string { return "127.0.0.1" }

func (c *countingListener) accepted() int64 { return c.n.Load() }

func (c *countingListener) close() {
	if c.l != nil {
		c.l.Close()
	}
}
