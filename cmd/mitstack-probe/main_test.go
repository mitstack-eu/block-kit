package main

// mitstack-probe exits 0 for an answer below 400, a redirect never followed, 1 for 400 or above, a refused connection or a timeout, and 2 for anything but one URL.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProbe_PassesBelow400NeverFollowingARedirectAndFailsOtherwise(t *testing.T) {
	gone := httptest.NewServer(nil)
	gone.Close()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(strings.Trim(r.URL.Path, "/"))
		if code == 0 {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Location", gone.URL+"/")
		w.WriteHeader(code)
	}))
	t.Cleanup(app.Close)
	for _, tc := range []struct {
		args, says string
		want       int
	}{
		{app.URL + "/200", "200 OK", 0},
		{app.URL + "/302", "302 Found", 0},
		{app.URL + "/399", "399", 0},
		{app.URL + "/400", "400 Bad Request", 1},
		{app.URL + "/404", "404 Not Found", 1},
		{app.URL + "/500", "500 Internal Server Error", 1},
		{gone.URL + "/", "connection refused", 1},
		{app.URL + "/slow", "Client.Timeout", 1},
		{"", "usage: mitstack-probe <url>", 2},
		{app.URL + "/200 " + app.URL + "/200", "usage: mitstack-probe <url>", 2},
	} {
		out := &bytes.Buffer{}
		if got := run(strings.Fields(tc.args), out, time.Second); got != tc.want || !strings.Contains(out.String(), tc.says) {
			t.Errorf("run(%q) = %d, printing %q; want %d and a line saying %q", tc.args, got, out, tc.want, tc.says)
		}
	}
}
