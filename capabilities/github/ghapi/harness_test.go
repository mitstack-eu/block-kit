package ghapi_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
)

const (
	token  = "ghs_super-secret-installation-token-999"
	widget = "/repos/acme/widget"
	gitops = "/repos/acme/gitops"
)

const (
	shaBase       = "0b95b71f5e6d0a8c4ad1e7f3c2b9a6d5e4f3a2b1"
	shaBaseTree   = "044b28c11d42ac5f2be6151bfdb013f46694c297"
	shaParentTree = "2ecf75eabe776947a73f03bf5fa7cc3574f6b69b"
	shaNewTree    = "5d890c4e1b2a3f4e5d6c7b8a9f0e1d2c3b4a5f6e"
	shaNewCommit  = "423bd6305a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d"
)

// mainSHA, mainTree and rebuiltTree are the commit main points at, its tree, and the tree rebuilt over it, at generation gen.
func mainSHA(gen int) string     { return fmt.Sprintf("a%039d", gen) }
func mainTree(gen int) string    { return fmt.Sprintf("b%039d", gen) }
func rebuiltTree(gen int) string { return fmt.Sprintf("c%039d", gen) }

// reply is one scripted answer: status 0 means 200, and {auth} in body echoes the request's Authorization header, as a proxy or a misconfigured host might.
type reply struct {
	status int
	body   string
}

var notFound = reply{http.StatusNotFound, `{"message":"Not Found"}`}

type call struct {
	key  string
	body map[string]any
	auth string
}

// fakeGitHub answers each "METHOD request-URI" from its routes, consuming replies in order with the last one repeating, and records every request.
type fakeGitHub struct {
	mu       sync.Mutex
	routes   map[string][]reply
	fallback *reply
	calls    []call
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.RequestURI()
	var body map[string]any
	decodeErr := json.NewDecoder(r.Body).Decode(&body)
	f.calls = append(f.calls, call{key: key, body: body, auth: r.Header.Get("Authorization")})
	if decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
		http.Error(w, "the fake GitHub could not decode the request body: "+decodeErr.Error(), http.StatusBadRequest)
		return
	}
	rep := notFound
	if replies, ok := f.routes[key]; ok {
		rep = replies[0]
		if len(replies) > 1 {
			f.routes[key] = replies[1:]
		}
	} else if f.fallback != nil {
		rep = *f.fallback
	}
	if rep.status != 0 {
		w.WriteHeader(rep.status)
	}
	_, _ = w.Write([]byte(strings.ReplaceAll(rep.body, "{auth}", r.Header.Get("Authorization"))))
}

func (f *fakeGitHub) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.key)
	}
	return out
}

// bodies returns the decoded bodies of every request to key, in order.
func (f *fakeGitHub) bodies(key string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.key == key {
			out = append(out, c.body)
		}
	}
	return out
}

func serve(t *testing.T, f *fakeGitHub) *ghapi.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return ghapi.NewClient(srv.URL, token, srv.Client())
}

func get(path string) string   { return "GET " + path }
func post(path string) string  { return "POST " + path }
func patch(path string) string { return "PATCH " + path }

func strp(s string) *string { return &s }

// jsonBody decodes a literal JSON body the way the fake decodes a request's.
func jsonBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return m
}

// errAny stands for any error at all where a row asserts no particular one.
var errAny = errors.New("any error")

// apiCase drives one Client call against scripted routes and pins its result, its error and every request it made, in order.
type apiCase struct {
	name     string
	routes   map[string][]reply
	call     func(c *ghapi.Client) (any, error)
	want     any
	err      error
	notErr   error
	errHas   string
	requests []string
	check    func(t *testing.T, f *fakeGitHub)
}

func runAPICases(t *testing.T, cases []apiCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGitHub{routes: tc.routes}
			c := serve(t, f)

			got, err := tc.call(c)

			switch {
			case tc.err == nil && err != nil:
				t.Fatalf("error = %v, want none", err)
			case tc.err != nil && err == nil:
				t.Fatalf("got %#v and no error, want %v", got, tc.err)
			case tc.err != nil && tc.err != errAny && !errors.Is(err, tc.err):
				t.Errorf("error = %v, want it to wrap %v", err, tc.err)
			case tc.notErr != nil && errors.Is(err, tc.notErr):
				t.Errorf("error = %v, want it not to wrap %v", err, tc.notErr)
			}
			if err != nil && (strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), tc.errHas)) {
				t.Errorf("error = %q, want it to say %q and never carry the token", err, tc.errHas)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
			if keys := f.keys(); !slices.Equal(keys, tc.requests) {
				t.Errorf("requests = %q\nwant       %q", keys, tc.requests)
			}
			for _, c := range f.calls {
				if c.auth != "Bearer "+token {
					t.Errorf("%s carried Authorization %q, want the client's bearer token", c.key, c.auth)
				}
			}
			if tc.check != nil {
				tc.check(t, f)
			}
		})
	}
}

// wrappedBase64 encodes content the way the contents API does, in 60-character lines.
func wrappedBase64(content string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(content))
	var b strings.Builder
	for len(enc) > 60 {
		b.WriteString(enc[:60] + "\n")
		enc = enc[60:]
	}
	b.WriteString(enc + "\n")
	return b.String()
}

// huge is valid JSON past any sane single-object bound, which an unbounded decoder reads fine.
func huge(fields string) string {
	return `{` + fields + `,"padding":"` + strings.Repeat("a", 2<<20) + `"}`
}

// treeAnswer is GitHub's create-tree answer for a root of n files, led by the tree's SHA as GitHub sends it, at about 231 bytes an entry.
func treeAnswer(sha string, n int) string {
	entries := make([]string, n)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"path":"process-%02d-deployment.json","mode":"100644","type":"blob","sha":"%040d","size":1024,`+
			`"url":"https://api.github.com/repos/acme/gitops/git/blobs/%040d"}`, i, i, i)
	}
	return `{"sha":"` + sha + `","url":"https://api.github.com/repos/acme/gitops/git/trees/` + sha + `","tree":[` + strings.Join(entries, ",") + `],"truncated":false}`
}
