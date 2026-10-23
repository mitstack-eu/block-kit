package kittest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// FakeSTS is a stand-in for AWS STS's AssumeRoleWithWebIdentity that fails loudly on any other action instead of answering plausibly.
type FakeSTS struct {
	*httptest.Server

	mu sync.Mutex
	// Calls records the role ARNs assumed, in order.
	Calls []string
	// FailWith makes every assume return this AWS error code.
	FailWith string
	// Expiry zero means one hour out.
	Expiry time.Time
}

// NewFakeSTS starts a fake STS endpoint that is closed when the test ends.
func NewFakeSTS(t *testing.T) *FakeSTS {
	t.Helper()
	f := &FakeSTS{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		action := r.FormValue("Action")
		if action != "AssumeRoleWithWebIdentity" {
			http.Error(w, fmt.Sprintf(
				"FakeSTS: unmodelled action %q — this fake implements AssumeRoleWithWebIdentity only. "+
					"If the operator genuinely needs this call, model it; do not make the fake permissive.", action),
				http.StatusNotImplemented)
			return
		}

		f.mu.Lock()
		f.Calls = append(f.Calls, r.FormValue("RoleArn"))
		failWith, expiry := f.FailWith, f.Expiry
		f.mu.Unlock()

		if failWith != "" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `<ErrorResponse><Error><Code>%s</Code><Message>%s</Message></Error></ErrorResponse>`, failWith, failWith)
			return
		}
		if expiry.IsZero() {
			expiry = time.Now().Add(time.Hour)
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials>`+
			`<AccessKeyId>AKIAFAKE</AccessKeyId><SecretAccessKey>fake-secret</SecretAccessKey>`+
			`<SessionToken>fake-session</SessionToken><Expiration>%s</Expiration>`+
			`</Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`,
			expiry.UTC().Format(time.RFC3339))
	}))
	t.Cleanup(f.Server.Close)
	return f
}

// AssumeCount reports how many assume-role calls were made.
func (f *FakeSTS) AssumeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}

// FakeGitHub is a stand-in for the GitHub App and REST calls operators make, and it fails loudly on any other call.
type FakeGitHub struct {
	*httptest.Server

	mu       sync.Mutex
	Requests []string
	// Repos that already exist return 422 on create, as GitHub does, which an operator's idempotency path must handle.
	Repos        map[string]bool
	Commits      []string
	TokenRevoked bool
}

// NewFakeGitHub starts a fake GitHub API that is closed when the test ends.
func NewFakeGitHub(t *testing.T) *FakeGitHub {
	t.Helper()
	f := &FakeGitHub{Repos: map[string]bool{}}

	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()

		if r.Header.Get("Authorization") == "" {
			http.Error(w, "FakeGitHub: no Authorization header — operators must send the installation token", http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_fake_installation_token",
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})

		case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
			f.mu.Lock()
			f.TokenRevoked = true
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/"):
			name := strings.TrimPrefix(r.URL.Path, "/repos/")
			f.mu.Lock()
			exists := f.Repos[name]
			f.mu.Unlock()
			if !exists {
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": name, "default_branch": "main"})

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repos"):
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			already := f.Repos[body.Name]
			f.Repos[body.Name] = true
			f.mu.Unlock()
			if already {
				http.Error(w, `{"message":"Repository creation failed: name already exists"}`, http.StatusUnprocessableEntity)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": body.Name})

		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/git/commits"):
			var body struct {
				Message string `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.Commits = append(f.Commits, body.Message)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": "fakecommitsha"})

		case strings.Contains(r.URL.Path, "/git/"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": "fakesha", "object": map[string]any{"sha": "fakesha"}})

		default:
			// A fake that answers everything makes every test pass.
			http.Error(w, fmt.Sprintf(
				"FakeGitHub: unmodelled call %s %s — this fake implements only what operators actually use. "+
					"Model the call rather than making the fake permissive: a fake that answers everything makes every test pass.",
				r.Method, r.URL.Path), http.StatusNotImplemented)
		}
	}))
	t.Cleanup(f.Server.Close)
	return f
}

// Saw reports whether a "METHOD /path" request was made.
func (f *FakeGitHub) Saw(method, path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.Requests {
		if r == method+" "+path {
			return true
		}
	}
	return false
}

// RequestCount reports how many requests were made.
func (f *FakeGitHub) RequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Requests)
}
