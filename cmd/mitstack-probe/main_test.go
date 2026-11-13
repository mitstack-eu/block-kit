package main

// mitstack-probe exits 0 for an answer below 400, a redirect judged by its own status and never followed, and 1 for an answer of 400 or above, a refused connection or an answer past its timeout.

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func answering(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL + "/"
}

// unreachable is an address nothing listens on, closed before the test reads it.
func unreachable(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr + "/"
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func TestProbe_JudgesTheAnswerByItsOwnStatus(t *testing.T) {
	nowhere := unreachable(t)
	for _, tc := range []struct {
		name   string
		answer http.HandlerFunc
		want   int
	}{
		{"a 200 passes", status(http.StatusOK), 0},
		{"a 302 to an address nothing listens on passes, since the probe never follows it", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, nowhere, http.StatusFound)
		}, 0},
		{"a 399 passes", status(399), 0},
		{"a 400 fails", status(http.StatusBadRequest), 1},
		{"a 404 fails", status(http.StatusNotFound), 1},
		{"a 500 fails", status(http.StatusInternalServerError), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, out := answering(t, tc.answer), &bytes.Buffer{}

			got := run([]string{url}, out, time.Second)

			if got != tc.want || !strings.Contains(out.String(), url) {
				t.Errorf("run(%s) = %d, printing %q; want %d and a line naming the URL", url, got, out, tc.want)
			}
		})
	}
}

func TestProbe_FailsWithoutAnAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, url, says string
	}{
		{"a port nothing listens on", unreachable(t), "connection refused"},
		{"an answer that comes past the timeout", answering(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }), "Client.Timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &bytes.Buffer{}

			got := run([]string{tc.url}, out, 50*time.Millisecond)

			if got != 1 || !strings.Contains(out.String(), tc.says) {
				t.Errorf("run(%s) = %d, printing %q; want 1 and a line saying %q", tc.url, got, out, tc.says)
			}
		})
	}
}

func TestProbe_RefusesAnythingButOneURL(t *testing.T) {
	for _, args := range [][]string{nil, {"http://127.0.0.1:8080/", "http://127.0.0.1:8081/"}} {
		out := &bytes.Buffer{}

		got := run(args, out, time.Second)

		if got != 2 || !strings.HasPrefix(out.String(), "usage: mitstack-probe <url>") {
			t.Errorf("run(%q) = %d, printing %q; want 2 and the usage line", args, got, out)
		}
	}
}
