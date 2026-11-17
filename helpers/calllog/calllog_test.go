// Each call through a calllog client writes one line naming its subscription, method, host, route, answer and milliseconds, and nothing the call carried.
package calllog_test

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot"
	"github.com/mitstack-eu/block-kit/helpers/calllog"
	"github.com/mitstack-eu/block-kit/helpers/events"
	"github.com/mitstack-eu/block-kit/helpers/lease"
)

const (
	token   = "tok-calllog-sentinel-5d1e"
	project = "3f2a9c1e-77b0-4c1e-9e55-1a2b3c4d5e6f"
	stall   = 50 * time.Millisecond
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

func lines(logs *bytes.Buffer) []map[string]string {
	var out []map[string]string
	for _, l := range strings.Split(logs.String(), "\n") {
		_, rest, ok := strings.Cut(l, "calllog: ")
		if !ok {
			continue
		}
		fields := map[string]string{}
		for _, kv := range strings.Fields(rest) {
			k, v, _ := strings.Cut(kv, "=")
			fields[k] = v
		}
		out = append(out, fields)
	}
	return out
}

// newServer answers status at once, or holds the call until its client gives up when status is zero.
func newServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == 0 {
			select {
			case <-r.Context().Done():
			case <-released:
			}
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(released) })
	return srv
}

func TestTransport_EachCallWritesOneLineNamingItsRouteAndAnswerAndNothingItCarried(t *testing.T) {
	cancelled := func(ctx context.Context) context.Context {
		ctx, cancel := context.WithCancel(ctx)
		time.AfterFunc(stall, cancel)
		return ctx
	}
	cases := []struct {
		name          string
		status        int
		method, path  string
		client        *http.Client
		ctx           func(context.Context) context.Context
		subscription  string
		route, answer string
		atLeast       time.Duration
	}{
		{name: "a GET answered 200 names its route with {id} and no query or header value", status: http.StatusOK, method: http.MethodGet,
			path: "/cockpit/v1/projects/" + project + "/data-sources?project_id=" + project + "&token=" + token, subscription: "sub_a",
			route: "/cockpit/v1/projects/{id}/data-sources", answer: "200"},
		{name: "a call answered 404 names the status", status: http.StatusNotFound, method: http.MethodDelete, path: "/v1/servers/42",
			subscription: "sub_a", route: "/v1/servers/{id}", answer: "404"},
		{name: "a call whose context is cancelled after 50 ms reads canceled", method: http.MethodGet, path: "/v1/servers", ctx: cancelled,
			subscription: "sub_a", route: "/v1/servers", answer: "canceled", atLeast: stall},
		{name: "a call that reaches the client timeout reads timeout", method: http.MethodGet, path: "/v1/servers", client: &http.Client{Timeout: stall},
			subscription: "sub_a", route: "/v1/servers", answer: "timeout", atLeast: stall},
		{name: "a call under no subscription names none", status: http.StatusOK, method: http.MethodGet, path: "/v1/servers",
			subscription: "-", route: "/v1/servers", answer: "200"},
		{name: "a client wrapped twice still writes one line", status: http.StatusOK, method: http.MethodGet, path: "/v1/servers",
			client: calllog.Client(&http.Client{}), subscription: "sub_a", route: "/v1/servers", answer: "200"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			srv := newServer(t, tc.status)
			client := &http.Client{}
			if tc.client != nil {
				client = tc.client
			}
			ctx := context.Background()
			if tc.subscription != "-" {
				ctx = calllog.WithSubscription(ctx, tc.subscription)
			}
			if tc.ctx != nil {
				ctx = tc.ctx(ctx)
			}
			req, _ := http.NewRequestWithContext(ctx, tc.method, srv.URL+tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-Auth-Token", token)

			if resp, err := calllog.Client(client).Do(req); err == nil {
				resp.Body.Close()
			}

			got := lines(logs)
			if len(got) != 1 {
				t.Fatalf("logs = %q; want exactly one calllog line", logs)
			}
			ms, err := strconv.Atoi(got[0]["ms"])
			if err != nil || time.Duration(ms)*time.Millisecond < tc.atLeast {
				t.Errorf("ms = %q; want a whole number of milliseconds of at least %d", got[0]["ms"], tc.atLeast.Milliseconds())
			}
			delete(got[0], "ms")
			want := map[string]string{"subscription": tc.subscription, "method": tc.method, "host": strings.TrimPrefix(srv.URL, "http://"), "route": tc.route, "answer": tc.answer}
			if !reflect.DeepEqual(got[0], want) || strings.Contains(logs.String(), token) || strings.Contains(logs.String(), project) {
				t.Errorf("line = %v in logs %q; want %v and neither the token nor the project id", got[0], logs, want)
			}
		})
	}
}

func TestRoute_NamesEachSegmentThatIsNotAWordOrAFixedSegmentAsAnId(t *testing.T) {
	cases := []struct{ name, path, want string }{
		{"words and a version are kept", "/cockpit/v1/regions/fr-par/alert-manager/contact-points", "/cockpit/v1/regions/fr-par/alert-manager/contact-points"},
		{"a number, a uuid and an escaped name read {id}", "/v1/servers/42/actions/" + project + "/a%20b", "/v1/servers/{id}/actions/{id}/{id}"},
		{"an empty path reads /", "", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := calllog.Route(tc.path); got != tc.want {
				t.Errorf("Route(%q) = %q; want %q", tc.path, got, tc.want)
			}
		})
	}
	if got, want := calllog.Route("/repos/acme/gitops/git/ref/heads/main", "repos", "git", "ref", "heads"), "/repos/{id}/{id}/git/ref/heads/{id}"; got != want {
		t.Errorf("Route with fixed segments = %q; want %q, a word outside them read as an id", got, want)
	}
	if got, want := calllog.Host(project+".metrics.cockpit.fr-par.scw.cloud:443"), "{id}.metrics.cockpit.fr-par.scw.cloud:443"; got != want {
		t.Errorf("Host = %q; want %q, a label that is no word or number read as an id", got, want)
	}
}

func TestTransport_TwoSubscriptionsCallingAtOnceEachNameTheirOwn(t *testing.T) {
	logs := captureLog(t)
	var arrived sync.WaitGroup
	arrived.Add(2)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { arrived.Done(); arrived.Wait() }))
	t.Cleanup(srv.Close)
	client := calllog.Client(&http.Client{})
	var calls sync.WaitGroup
	for sub, path := range map[string]string{"sub_a": "/alpha", "sub_b": "/beta"} {
		calls.Add(1)
		go func() {
			defer calls.Done()
			req, _ := http.NewRequestWithContext(events.WithEnvelope(context.Background(), events.Envelope{SubscriptionID: sub}), http.MethodGet, srv.URL+path, nil)
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
			}
		}()
	}

	calls.Wait()

	got := map[string]string{}
	for _, l := range lines(logs) {
		got[l["subscription"]] = l["route"]
	}
	if want := map[string]string{"sub_a": "/alpha", "sub_b": "/beta"}; !reflect.DeepEqual(got, want) {
		t.Errorf("subscription to route = %v in logs %q; want %v", got, logs, want)
	}
}

func TestKitClients_EachWritesOneLineForOneCall(t *testing.T) {
	cases := []struct {
		name, method, route string
		call                func(ctx context.Context, base string)
	}{
		{"the Hetzner Cloud client", http.MethodGet, "/v1/primary_ips", func(ctx context.Context, base string) {
			_ = cloud.NewAt(base+"/v1", token).KeepPrimaryIPs(ctx, map[string]string{"subscription": "sub_a"})
		}},
		{"the Hetzner Robot client", http.MethodGet, "/server/{id}", func(ctx context.Context, base string) {
			_, _ = robot.NewAt(base, "robot-user", token).GetServer(ctx, 321)
		}},
		{"the GitHub client", http.MethodGet, "/repos/{id}/{id}/git/ref/heads/{id}", func(ctx context.Context, base string) {
			_, _ = ghapi.NewClient(base, token, nil).HeadCommit(ctx, "acme/gitops")
		}},
		{"the lease client", http.MethodPost, "/v1/cluster-leases", func(ctx context.Context, base string) {
			_, _ = lease.Platform{BaseURL: base, Token: token}.Ask(ctx, "scaleway.snapshot-bucket", nil)
		}},
		{"the events client", http.MethodPost, "/v1/subscriptions/{id}/events", func(ctx context.Context, base string) {
			env := events.Envelope{SubscriptionID: "sub_a", Callback: &events.Callback{URL: base + "/v1/subscriptions/sub_a7f3/events", Token: token}}
			_ = events.EmitProgress(events.WithEnvelope(ctx, env), "creating")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			srv := newServer(t, http.StatusOK)

			tc.call(calllog.WithSubscription(context.Background(), "sub_a"), srv.URL)

			got := lines(logs)
			host, _ := url.Parse(srv.URL)
			if len(got) != 1 || got[0]["subscription"] != "sub_a" || got[0]["method"] != tc.method || got[0]["host"] != host.Host || got[0]["route"] != tc.route || strings.Contains(logs.String(), token) {
				t.Errorf("logs = %q; want one line naming sub_a, %s, %s and route %s, and not the token", logs, tc.method, host.Host, tc.route)
			}
		})
	}
}
