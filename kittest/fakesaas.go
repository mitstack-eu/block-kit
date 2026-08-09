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

// FakeSaaS (MIT-1657) generalises the MIT-1138 Tier-1 hermetic-fake pattern
// (see the package note in fakes.go) to arbitrary SaaS providers: instead of a
// hand-rolled httptest.Handler per operator, a provider is modelled as a list
// of SaaSFixture request/response pairs, usually loaded from testdata via
// LoadSaaSFixtures. The operator's real client code runs against it unchanged.
//
// The same "fail loudly on the unmodelled" philosophy applies: a request that
// matches no fixture never returns a plausible 200, it returns 418 naming
// every fixture that was tried. And because a non-matching request is exactly
// the shape a probing/hostile caller sends, that diagnostic — and the call
// log FakeSaaS keeps for assertions — carries header NAMES only, never
// values, so a bearer token or API key can never leak through it.

// SaaSFixture is one recorded provider exchange: a request shape and the
// response FakeSaaS serves when a request matches it.
type SaaSFixture struct {
	Name     string              `json:"name"`
	Request  SaaSFixtureRequest  `json:"request"`
	Response SaaSFixtureResponse `json:"response"`
}

// SaaSFixtureRequest describes which requests this fixture matches. Method
// and Path must match exactly (Path is compared against the request's full
// RequestURI, so the query string is significant). Headers lists required
// headers: a value of "*" means "present and non-empty", any other value
// must match exactly.
type SaaSFixtureRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

// SaaSFixtureResponse is served verbatim when its fixture matches: Headers
// are set, then Status is written, then Body is written as-is.
type SaaSFixtureResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// RecordedSaaSCall is one request FakeSaaS received, matched or not. Only
// header NAMES are kept (canonicalized, sorted) — never values — so asserting
// against Calls() can never leak a secret header value.
type RecordedSaaSCall struct {
	Method      string
	Path        string
	HeaderNames []string
}

// FakeSaaS is a fixture-driven stand-in for a SaaS provider's HTTP API.
type FakeSaaS struct {
	// URL is the fake server's base URL.
	URL string

	server *httptest.Server

	mu       sync.Mutex
	fixtures []SaaSFixture
	calls    []RecordedSaaSCall
}

// NewFakeSaaS starts a fake SaaS endpoint served from fixtures, in list
// order (first match wins). It is closed when the test ends.
func NewFakeSaaS(t testing.TB, fixtures []SaaSFixture) *FakeSaaS {
	t.Helper()
	// Copy the fixture list: handle() reads it without holding mu, which is
	// only safe because nothing can mutate it after construction — including
	// a caller appending to the slice it passed in.
	f := &FakeSaaS{fixtures: append([]SaaSFixture(nil), fixtures...)}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	f.URL = f.server.URL
	t.Cleanup(f.server.Close)
	return f
}

// Calls returns every request FakeSaaS received, matched or not, in request
// order. Header values are never included — see the package note above.
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

// LoadSaaSFixtures loads every *.json file in dir, in filename order, as a
// SaaSFixture. Decoding is strict (an unrecognised field fails the load
// rather than silently no-opping a typo'd fixture forever), Method/Path/
// Status are required, and a directory with zero fixtures fails too — a
// provider with no fake coverage is exactly the condition the upstream lint
// gate flags, so the loader refuses it rather than starting a FakeSaaS with
// nothing to serve.
func LoadSaaSFixtures(t testing.TB, dir string) []SaaSFixture {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		// The `return nil` after each Fatalf is unreachable on *testing.T
		// (Fatalf never returns) but load-bearing for any testing.TB whose
		// Fatalf does not Goexit — do not "simplify" it away.
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
