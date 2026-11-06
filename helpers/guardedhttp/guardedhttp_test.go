package guardedhttp_test

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/guardedhttp"
)

// vendor stands a TLS server in place of the vendor, reached at 203.0.113.10:443 by the name api.example.com, and records every connection the client opens.
type vendor struct {
	server  *httptest.Server
	pool    *x509.CertPool
	mu      sync.Mutex
	dialed  []string
	resolve map[string][]net.IP
}

func newVendor(t *testing.T, handler http.HandlerFunc) *vendor {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &vendor{server: srv, pool: pool, resolve: map[string][]net.IP{"api.example.com": {net.ParseIP("203.0.113.10")}}}
}

func (v *vendor) client(t *testing.T, hosts ...string) *http.Client {
	t.Helper()
	c, err := guardedhttp.New(guardedhttp.Config{
		Hosts:   hosts,
		RootCAs: v.pool,
		Resolve: func(_ context.Context, host string) ([]net.IP, error) {
			v.mu.Lock()
			defer v.mu.Unlock()
			v.dialed = append(v.dialed, "resolve "+host)
			return v.resolve[host], nil
		},
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			v.mu.Lock()
			v.dialed = append(v.dialed, "dial "+address)
			v.mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, v.server.Listener.Addr().String())
		},
	})
	if err != nil {
		t.Fatalf("guardedhttp.New: %v", err)
	}
	return c
}

func (v *vendor) calls() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.dialed...)
}

func get(t *testing.T, c *http.Client, url string) (*http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(req)
}

func TestClient_ReachesADeclaredHostOverHTTPSAtTheAddressItChecked(t *testing.T) {
	v := newVendor(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") })
	c := v.client(t, "api.example.com")

	resp, err := get(t, c, "https://api.example.com/ping")

	if err != nil {
		t.Fatalf("GET a declared host: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "pong" {
		t.Errorf("body = %q, want pong", body)
	}
	if got, want := v.calls(), []string{"resolve api.example.com", "dial 203.0.113.10:443"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("the client resolved and dialed %q, want %q: the dial goes to the checked address, never the name", got, want)
	}
}

// Each address is a declared host, so the refusal is the address guard's alone.
func TestClient_RefusesEachAddressBeforeAConnectionOpens(t *testing.T) {
	addresses := []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "[::1]", "[fe80::1]", "[ff02::1]", "0.0.0.0", "[::ffff:10.0.0.1]"}
	for _, host := range addresses {
		t.Run(host, func(t *testing.T) {
			v := newVendor(t, func(http.ResponseWriter, *http.Request) { t.Error("the vendor was reached") })
			c := v.client(t, trimBrackets(host))

			_, err := get(t, c, "https://"+host+"/")

			if !errors.Is(err, guardedhttp.ErrRefused) {
				t.Errorf("GET https://%s/ error = %v, want guardedhttp.ErrRefused", host, err)
			}
			if got := v.calls(); len(got) != 0 {
				t.Errorf("the client touched %q for %s, want no resolve and no dial", got, host)
			}
		})
	}
}

func trimBrackets(host string) string {
	if len(host) > 1 && host[0] == '[' {
		return host[1 : len(host)-1]
	}
	return host
}

func TestClient_RefusesAHostThatResolvesToARefusedAddress(t *testing.T) {
	for name, ips := range map[string][]net.IP{
		"private":                  {net.ParseIP("10.1.2.3")},
		"loopback":                 {net.ParseIP("127.0.0.1")},
		"link-local metadata":      {net.ParseIP("169.254.169.254")},
		"one good and one private": {net.ParseIP("203.0.113.10"), net.ParseIP("192.168.0.9")},
		"nothing":                  nil,
	} {
		t.Run(name, func(t *testing.T) {
			v := newVendor(t, func(http.ResponseWriter, *http.Request) { t.Error("the vendor was reached") })
			v.resolve["api.example.com"] = ips
			c := v.client(t, "api.example.com")

			_, err := get(t, c, "https://api.example.com/")

			if !errors.Is(err, guardedhttp.ErrRefused) {
				t.Errorf("error = %v, want guardedhttp.ErrRefused", err)
			}
			for _, call := range v.calls() {
				if call[:4] == "dial" {
					t.Errorf("the client dialed %q, want no connection", call)
				}
			}
		})
	}
}

func TestClient_RefusesPlainHTTPBeforeResolvingAnything(t *testing.T) {
	v := newVendor(t, func(http.ResponseWriter, *http.Request) { t.Error("the vendor was reached") })
	c := v.client(t, "api.example.com")

	_, err := get(t, c, "http://api.example.com/")

	if !errors.Is(err, guardedhttp.ErrRefused) {
		t.Errorf("GET http:// error = %v, want guardedhttp.ErrRefused", err)
	}
	if got := v.calls(); len(got) != 0 {
		t.Errorf("the client touched %q, want nothing", got)
	}
}

func TestClient_RefusesAHostTheOperatorDidNotDeclare(t *testing.T) {
	v := newVendor(t, func(http.ResponseWriter, *http.Request) { t.Error("the vendor was reached") })
	c := v.client(t, "api.example.com")

	for _, url := range []string{"https://other.example/", "https://api.example.com.evil.example/", "https://user:pw@api.example.com/"} {
		_, err := get(t, c, url)

		if !errors.Is(err, guardedhttp.ErrRefused) {
			t.Errorf("GET %s error = %v, want guardedhttp.ErrRefused", url, err)
		}
	}
	if got := v.calls(); len(got) != 0 {
		t.Errorf("the client touched %q, want nothing", got)
	}
}

func TestClient_DoesNotFollowARedirect(t *testing.T) {
	v := newVendor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://api.example.com/elsewhere", http.StatusFound)
	})
	c := v.client(t, "api.example.com")

	_, err := get(t, c, "https://api.example.com/")

	if !errors.Is(err, guardedhttp.ErrRefused) {
		t.Errorf("GET a redirect error = %v, want guardedhttp.ErrRefused", err)
	}
	dials := 0
	for _, call := range v.calls() {
		if call[:4] == "dial" {
			dials++
		}
	}
	if dials != 1 {
		t.Errorf("the client dialed %d times (%q), want only the first request's connection", dials, v.calls())
	}
}

func TestNew_RefusesAConfigThatCouldReachNothingOrEverything(t *testing.T) {
	for name, hosts := range map[string][]string{
		"no host":       nil,
		"an empty host": {""},
		"a URL":         {"https://api.example.com"},
		"a host+port":   {"api.example.com:443"},
		"a wildcard":    {"*.api.example.com"},
		"a path":        {"api.example.com/api"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := guardedhttp.New(guardedhttp.Config{Hosts: hosts}); err == nil {
				t.Errorf("guardedhttp.New with hosts %q succeeded, want an error", hosts)
			}
		})
	}
}

func TestClient_UsesNoProxyFromTheEnvironment(t *testing.T) {
	v := newVendor(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("https_proxy", "http://127.0.0.1:9")
	c := v.client(t, "api.example.com")

	resp, err := get(t, c, "https://api.example.com/")

	if err != nil {
		t.Fatalf("GET with HTTPS_PROXY set: %v", err)
	}
	resp.Body.Close()
}
