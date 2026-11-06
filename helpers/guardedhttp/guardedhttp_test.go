package guardedhttp_test

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/guardedhttp"
)

const vendorHost = "api.example.com"

// vendor stands a TLS server in place of the vendor, reached by name at 203.0.113.10, and records each resolve and dial.
type vendor struct {
	mu    sync.Mutex
	calls []string
	ips   []net.IP
	pool  *x509.CertPool
	addr  string
}

func newVendor(t *testing.T, handler http.HandlerFunc) *vendor {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &vendor{ips: []net.IP{net.ParseIP("203.0.113.10")}, pool: pool, addr: srv.Listener.Addr().String()}
}

func (v *vendor) record(call string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, call)
}

func (v *vendor) dials() (n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, c := range v.calls {
		if strings.HasPrefix(c, "dial") {
			n++
		}
	}
	return n
}

func (v *vendor) get(t *testing.T, url string, hosts ...string) (*http.Response, error) {
	t.Helper()
	c, err := guardedhttp.New(guardedhttp.Config{
		Hosts:   hosts,
		RootCAs: v.pool,
		Resolve: func(_ context.Context, host string) ([]net.IP, error) { v.record("resolve " + host); return v.ips, nil },
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			v.record("dial " + address)
			return (&net.Dialer{}).DialContext(ctx, network, v.addr)
		},
	})
	if err != nil {
		t.Fatalf("guardedhttp.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	return c.Do(req)
}

func TestClient_ReachesADeclaredHostOverHTTPSAtTheAddressItChecked(t *testing.T) {
	v := newVendor(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") })

	resp, err := v.get(t, "https://"+vendorHost+"/ping", vendorHost)

	if err != nil {
		t.Fatalf("GET a declared host: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if want := "resolve api.example.com,dial 203.0.113.10:443"; string(body) != "pong" || strings.Join(v.calls, ",") != want {
		t.Errorf("body %q, calls %q; want pong and %q: the dial goes to the checked address, never the name", body, v.calls, want)
	}
}

// An address literal is declared as a host, so only the address guard can refuse it.
func TestClient_RefusesBeforeAConnectionOpens(t *testing.T) {
	redirect := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+vendorHost+"/next", http.StatusFound)
	}
	cases := []struct {
		name, url string
		hosts     []string
		resolves  []net.IP
		wantDials int
	}{
		{"http", "http://" + vendorHost + "/", []string{vendorHost}, nil, 0},
		{"an undeclared host", "https://other.example/", []string{vendorHost}, nil, 0},
		{"a lookalike host", "https://" + vendorHost + ".evil.example/", []string{vendorHost}, nil, 0},
		{"credentials in the URL", "https://u:pw@" + vendorHost + "/", []string{vendorHost}, nil, 0},
		{"127.0.0.1", "https://127.0.0.1/", []string{"127.0.0.1"}, nil, 0},
		{"10.0.0.1", "https://10.0.0.1/", []string{"10.0.0.1"}, nil, 0},
		{"169.254.169.254", "https://169.254.169.254/", []string{"169.254.169.254"}, nil, 0},
		{"::1", "https://[::1]/", []string{"::1"}, nil, 0},
		{"fe80::1", "https://[fe80::1]/", []string{"fe80::1"}, nil, 0},
		{"ff02::1", "https://[ff02::1]/", []string{"ff02::1"}, nil, 0},
		{"0.0.0.0", "https://0.0.0.0/", []string{"0.0.0.0"}, nil, 0},
		{"::ffff:10.0.0.1", "https://[::ffff:10.0.0.1]/", []string{"::ffff:10.0.0.1"}, nil, 0},
		{"a name resolving to a private address", "https://" + vendorHost + "/", []string{vendorHost}, []net.IP{net.ParseIP("10.1.2.3")}, 0},
		{"a name resolving to loopback", "https://" + vendorHost + "/", []string{vendorHost}, []net.IP{net.ParseIP("127.0.0.1")}, 0},
		{"a name resolving to one good and one private address", "https://" + vendorHost + "/", []string{vendorHost}, []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("192.168.0.9")}, 0},
		{"a redirect", "https://" + vendorHost + "/", []string{vendorHost}, nil, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newVendor(t, redirect)
			if tc.resolves != nil {
				v.ips = tc.resolves
			}

			_, err := v.get(t, tc.url, tc.hosts...)

			if !errors.Is(err, guardedhttp.ErrRefused) {
				t.Errorf("GET %s error = %v, want guardedhttp.ErrRefused", tc.url, err)
			}
			if got := v.dials(); got != tc.wantDials {
				t.Errorf("the client dialed %d times (%q), want %d", got, v.calls, tc.wantDials)
			}
		})
	}
}

func TestNew_RefusesAConfigThatCouldReachNothingOrEverything(t *testing.T) {
	for _, hosts := range [][]string{nil, {""}, {"https://api.example.com"}, {"api.example.com:443"}, {"*.example.com"}, {"api.example.com/v2"}} {
		if _, err := guardedhttp.New(guardedhttp.Config{Hosts: hosts}); err == nil {
			t.Errorf("guardedhttp.New with hosts %q succeeded, want an error", hosts)
		}
	}
}

func TestClient_UsesNoProxyFromTheEnvironment(t *testing.T) {
	v := newVendor(t, func(w http.ResponseWriter, _ *http.Request) {})
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")

	resp, err := v.get(t, "https://"+vendorHost+"/", vendorHost)

	if err != nil {
		t.Fatalf("GET with HTTPS_PROXY set: %v", err)
	}
	resp.Body.Close()
}
