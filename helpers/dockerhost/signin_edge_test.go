//go:build edgecaddy

package dockerhost_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/helpers/signin"
)

// seen keeps the headers and body of each request a stand-in server received.
type seen struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
}

func (s *seen) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r)
	s.bodies = append(s.bodies, string(body))
}

func (s *seen) last() (*http.Request, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return nil, "", 0
	}
	return s.requests[len(s.requests)-1], s.bodies[len(s.bodies)-1], len(s.requests)
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func portOf(s *httptest.Server) string { return s.URL[strings.LastIndex(s.URL, ":")+1:] }

// TestEdge_ARealCaddyRunsTheSignInRoute renders the route with Site and runs it in the caddy image CADDY_IMAGE
// names, on the host's network, in front of a stand-in app and a stand-in companion.
func TestEdge_ARealCaddyRunsTheSignInRoute(t *testing.T) {
	image := os.Getenv("CADDY_IMAGE")
	if image == "" {
		t.Fatal("CADDY_IMAGE names no caddy image")
	}
	var app, companion seen
	appServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.record(r)
		_, _ = io.WriteString(w, "the app")
	}))
	t.Cleanup(appServer.Close)
	companionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		companion.record(r)
		switch {
		case r.URL.Path != "/.mitstack/check":
			_, _ = io.WriteString(w, "the companion")
		case strings.Contains(r.Header.Get("Cookie"), "member=yes"):
			w.Header().Set("X-Mitstack-User", "user_3f9c2a1b")
			w.Header().Set("X-Mitstack-Email", "yael@example.com")
			w.WriteHeader(http.StatusOK)
		default:
			http.Redirect(w, r, "/.mitstack/start?return=%2F", http.StatusFound)
		}
	}))
	t.Cleanup(companionServer.Close)

	route := costs
	route.Upstream = "localhost:" + portOf(appServer)
	site, err := dockerhost.Edge{PublicIP: publicIP, Compress: true}.Site(route)
	if err != nil {
		t.Fatal(err)
	}
	edgePort := freePort(t)
	site = strings.Replace(site, signin.Upstream, "localhost:"+portOf(companionServer), 2)
	site = strings.Replace(site, route.Hostname+" {", "http://"+route.Hostname+":"+strconv.Itoa(edgePort)+" {", 1)
	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n  admin off\n  auto_https off\n}\n"+site), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", "--network", "host",
		"-v", caddyfile+":/etc/caddy/Caddyfile:ro", image).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run %s: %v\n%s", image, err, out)
	}
	container := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", container).CombinedOutput()
			t.Logf("caddy logs:\n%s", logs)
		}
		_ = exec.Command("docker", "rm", "-f", container).Run()
	})

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(method, path, cookie, body string) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(edgePort)+path, strings.NewReader(body))
		r.Host = route.Hostname
		for _, name := range signin.MemberHeaders {
			r.Header.Set(name, "posed")
		}
		r.Header.Set(signin.AppHeader, "app_posed")
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		res, err := client.Do(r)
		if err != nil {
			t.Fatalf("%s %s through caddy: %v", method, path, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(edgePort)); err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("caddy did not listen on %d within 30 seconds", edgePort)
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Run("a member reaches the app with only the companion's headers", func(t *testing.T) {
		res := send(http.MethodPost, "/plan", "member=yes", "week=3")
		r, body, _ := app.last()
		if res.StatusCode != http.StatusOK || r == nil {
			t.Fatalf("a member's POST answered %d and reached the app %v; want 200 from the app", res.StatusCode, r != nil)
		}
		want := map[string]string{"X-Mitstack-User": "user_3f9c2a1b", "X-Mitstack-Email": "yael@example.com",
			"X-Mitstack-Name": "", "X-Mitstack-Org": "", "X-Mitstack-Role": ""}
		for name, value := range want {
			if got := r.Header.Values(name); strings.Join(got, ",") != value {
				t.Errorf("the app received %s %q; want %q", name, got, value)
			}
		}
		if r.Method != http.MethodPost || body != "week=3" {
			t.Errorf("the app received %s with body %q; want the POST with body week=3", r.Method, body)
		}
	})

	t.Run("a visitor with no app session never reaches the app", func(t *testing.T) {
		_, _, before := app.last()
		res := send(http.MethodGet, "/plan", "", "")
		_, _, after := app.last()
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/.mitstack/start?return=%2F" || after != before {
			t.Errorf("a visitor with no session answered %d to %q and reached the app %d times; want the companion's 302 and none",
				res.StatusCode, res.Header.Get("Location"), after-before)
		}
		r, _, _ := companion.last()
		if r.URL.Path != "/.mitstack/check" || r.Header.Get("X-Forwarded-Method") != http.MethodGet || r.Header.Get("X-Forwarded-Uri") != "/plan" {
			t.Errorf("the companion was asked %s with method %q and uri %q; want /.mitstack/check about GET /plan",
				r.URL.Path, r.Header.Get("X-Forwarded-Method"), r.Header.Get("X-Forwarded-Uri"))
		}
	})

	t.Run("a path under /.mitstack/ goes to the companion, told the route's app", func(t *testing.T) {
		_, _, before := app.last()
		res := send(http.MethodGet, "/.mitstack/callback?code=c&state=s", "", "")
		r, _, _ := companion.last()
		_, _, after := app.last()
		if res.StatusCode != http.StatusOK || after != before || r.URL.Path != "/.mitstack/callback" {
			t.Fatalf("/.mitstack/callback answered %d, reached the app %d times and the companion at %s; want the companion only",
				res.StatusCode, after-before, r.URL.Path)
		}
		for name, value := range map[string]string{
			signin.AppHeader:        "app_8d21b0c4",
			signin.SignInHostHeader: "costs.apps.mitstack.dev",
			signin.HostsHeader:      "costs.apps.mitstack.dev shop.example.com",
		} {
			if got := r.Header.Values(name); strings.Join(got, ",") != value {
				t.Errorf("the companion received %s %q; want %q", name, got, value)
			}
		}
		for _, name := range signin.MemberHeaders {
			if got := r.Header.Values(name); len(got) > 0 {
				t.Errorf("the companion received the visitor's %s %q; want the edge to delete it", name, got)
			}
		}
	})
}
