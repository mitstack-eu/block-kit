package kittest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// SaaSFixture is one recorded provider exchange: a request shape and the response served when a request matches it.
type SaaSFixture struct {
	Name     string              `json:"name"`
	Request  SaaSFixtureRequest  `json:"request"`
	Response SaaSFixtureResponse `json:"response"`
}

// SaaSFixtureRequest matches Method and the full RequestURI exactly, and a Headers value of "*" means present and non-empty.
type SaaSFixtureRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

// SaaSFixtureResponse is served verbatim when its fixture matches.
type SaaSFixtureResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// RecordedSaaSCall keeps header names only, never values, so asserting against Calls() cannot leak a secret.
type RecordedSaaSCall struct {
	Method      string
	Path        string
	HeaderNames []string
}

// FakeSaaS is a fixture-driven stand-in for a SaaS provider's HTTP API, answering 418 to any request matching no fixture.
type FakeSaaS struct {
	URL string

	server *httptest.Server

	mu       sync.Mutex
	fixtures []SaaSFixture
	calls    []RecordedSaaSCall
}

// NewFakeSaaS starts a fake SaaS endpoint served from fixtures where the first match wins, closed when the test ends.
func NewFakeSaaS(t testing.TB, fixtures []SaaSFixture) *FakeSaaS {
	t.Helper()
	// The list is copied because handle() reads it without holding mu.
	f := &FakeSaaS{fixtures: append([]SaaSFixture(nil), fixtures...)}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	f.URL = f.server.URL
	t.Cleanup(f.server.Close)
	return f
}

// Calls returns every request received, matched or not, in request order.
func (f *FakeSaaS) Calls() []RecordedSaaSCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RecordedSaaSCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *FakeSaaS) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, RecordedSaaSCall{
		Method:      r.Method,
		Path:        r.URL.RequestURI(),
		HeaderNames: canonicalHeaderNames(r.Header),
	})
	f.mu.Unlock()

	for _, fx := range f.fixtures {
		if !fixtureMatches(fx, r) {
			continue
		}
		for k, v := range fx.Response.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(fx.Response.Status)
		if len(fx.Response.Body) > 0 {
			_, _ = w.Write(fx.Response.Body)
		}
		return
	}

	tried := make([]string, 0, len(f.fixtures))
	for _, fx := range f.fixtures {
		tried = append(tried, fx.Name)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTeapot)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"method":        r.Method,
		"path":          r.URL.RequestURI(),
		"fixturesTried": tried,
	})
}

func fixtureMatches(fx SaaSFixture, r *http.Request) bool {
	if fx.Request.Method != r.Method {
		return false
	}
	if fx.Request.Path != r.URL.RequestURI() {
		return false
	}
	for name, want := range fx.Request.Headers {
		got := r.Header.Get(name)
		if want == "*" {
			if got == "" {
				return false
			}
			continue
		}
		if got != want {
			return false
		}
	}
	return true
}

func canonicalHeaderNames(h http.Header) []string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, textproto.CanonicalMIMEHeaderKey(k))
	}
	sort.Strings(names)
	return names
}

// LoadSaaSFixtures loads every *.json file in dir in filename order, and fails on an unknown field or an empty directory rather than serving nothing.
func LoadSaaSFixtures(t testing.TB, dir string) []SaaSFixture {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		// The return nil after each Fatalf matters for a testing.TB whose Fatalf does not Goexit.
		t.Fatalf("LoadSaaSFixtures: read dir %s: %v", dir, err)
		return nil
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatalf("LoadSaaSFixtures: no *.json fixtures found in %s — a provider with zero fixtures "+
			"is refused, not silently accepted", dir)
		return nil
	}

	fixtures := make([]SaaSFixture, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("LoadSaaSFixtures: open %s: %v", path, err)
			return nil
		}

		var fx SaaSFixture
		dec := json.NewDecoder(file)
		dec.DisallowUnknownFields()
		err = dec.Decode(&fx)
		_ = file.Close()
		if err != nil {
			t.Fatalf("LoadSaaSFixtures: decode %s: %v", path, err)
			return nil
		}

		if fx.Request.Method == "" {
			t.Fatalf("LoadSaaSFixtures: %s: request.method is required", path)
			return nil
		}
		if fx.Request.Path == "" {
			t.Fatalf("LoadSaaSFixtures: %s: request.path is required", path)
			return nil
		}
		if fx.Response.Status == 0 {
			t.Fatalf("LoadSaaSFixtures: %s: response.status is required", path)
			return nil
		}

		fixtures = append(fixtures, fx)
	}
	return fixtures
}
