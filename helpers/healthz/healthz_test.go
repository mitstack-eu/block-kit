// An operator mounting the kit's health handler answers 200 ok on /healthz to every method a probe uses.
package healthz_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/healthz"
)

func TestHandler_AnswersOKOnTheHealthPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(healthz.Path, healthz.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, err := http.NewRequest(method, srv.URL+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s /healthz answered %d, want 200", method, resp.StatusCode)
		}
		if want := map[string]string{http.MethodGet: "ok", http.MethodHead: ""}[method]; string(body) != want {
			t.Errorf("%s /healthz answered body %q, want %q", method, body, want)
		}
	}
}

func TestHandler_LeavesEveryOtherPathToTheMux(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(healthz.Path, healthz.Handler())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/extra", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /healthz/extra answered %d, want 404, since the handler owns the exact path alone", rec.Code)
	}
}
