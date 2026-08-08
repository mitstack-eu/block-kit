package ghapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/ghapi"
)

// newTestClient points a Client at an httptest fixture. Client.Base is a
// plain, already-resolved string — production callers build it via
// ghapi.Base(endpoint, extraHosts...).String() (base_test.go pins that
// resolution step); by the time it reaches Client the trust decision has
// already been made once, so a test fixture can supply srv.URL directly with
// no separate carve-out anywhere in the trust boundary itself.
func newTestClient(srv *httptest.Server, token string) *ghapi.Client {
	return ghapi.NewClient(srv.URL, token, srv.Client())
}

// --- EnsureRepo is idempotent (GET-then-create) ------------------------------

func TestEnsureRepo_MakesNoCreateCallWhenRepoAlreadyExists(t *testing.T) {
	var getCalls, createCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget":
			getCalls++
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/acme/repos":
			createCalls++
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv, "test-token-abc123")

	full, err := c.EnsureRepo(context.Background(), "acme", "widget")
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if full != "acme/widget" {
		t.Errorf("full name = %q, want %q", full, "acme/widget")
	}
	if getCalls != 1 {
		t.Errorf("GET /repos calls = %d, want 1", getCalls)
	}
	if createCalls != 0 {
		t.Errorf("POST create calls = %d, want 0 — the repo already exists", createCalls)
	}
}

func TestEnsureRepo_CreatesWhenRepoIsMissing(t *testing.T) {
	var createCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/acme/repos":
			createCalls++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"full_name": "acme/widget"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv, "test-token-abc123")

	full, err := c.EnsureRepo(context.Background(), "acme", "widget")
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if full != "acme/widget" {
		t.Errorf("full name = %q, want %q", full, "acme/widget")
	}
	if createCalls != 1 {
		t.Errorf("create calls = %d, want exactly 1", createCalls)
	}
}

// --- DeleteRepo: 404 is success (MIT-1302 / MIT-1124 teardown contract) -----

func TestDeleteRepo_AbsentRepoIsSuccessNotError(t *testing.T) {
	var deleteCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/repos/acme/widget" {
			deleteCalls++
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := newTestClient(srv, "test-token-abc123")

	if err := c.DeleteRepo(context.Background(), "acme", "widget"); err != nil {
		t.Errorf("DeleteRepo on an already-absent repo must succeed (404 is success), got: %v", err)
	}
	if deleteCalls != 1 {
		t.Errorf("delete calls = %d, want 1", deleteCalls)
	}
}

func TestDeleteRepo_PropagatesARealFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"insufficient permissions"}`))
	}))
	defer srv.Close()
	c := newTestClient(srv, "test-token-abc123")

	if err := c.DeleteRepo(context.Background(), "acme", "widget"); err == nil {
		t.Error("a real (non-404) failure must be surfaced, not swallowed as if it were a successful teardown")
	}
}

// --- CommitFiles: unchanged tree is a TRUE no-op (MIT-1230) ------------------

func TestCommitFiles_NoOpWhenTheTreeIsUnchanged(t *testing.T) {
	var commitCalls, refPatches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget/git/ref/heads/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "base-sha"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget/git/commits/base-sha":
			_ = json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree-sha"}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/git/trees":
			// GitHub returns the SAME tree sha when the posted content is
			// byte-identical to base_tree.
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "tree-sha"})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/git/commits":
			commitCalls++
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/widget/git/refs/heads/main":
			refPatches++
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv, "test-token-abc123")

	err := c.CommitFiles(context.Background(), "acme/widget", "reconcile", map[string]string{"a.txt": "same content"})
	if err != nil {
		t.Fatalf("CommitFiles: %v", err)
	}
	if commitCalls != 0 {
		t.Errorf("commit POSTs = %d, want 0 — an unchanged tree must be a true no-op (MIT-1230)", commitCalls)
	}
	if refPatches != 0 {
		t.Errorf("ref PATCHes = %d, want 0 — the ref must not move when nothing changed", refPatches)
	}
}

func TestCommitFiles_CommitsAndMovesTheRefWhenTheTreeChanges(t *testing.T) {
	var commitCalls, refPatches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget/git/ref/heads/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "base-sha"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget/git/commits/base-sha":
			_ = json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree-sha"}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/git/trees":
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "new-tree-sha"})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/git/commits":
			commitCalls++
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "new-commit-sha"})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/widget/git/refs/heads/main":
			refPatches++
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv, "test-token-abc123")

	err := c.CommitFiles(context.Background(), "acme/widget", "reconcile", map[string]string{"a.txt": "new content"})
	if err != nil {
		t.Fatalf("CommitFiles: %v", err)
	}
	if commitCalls != 1 {
		t.Errorf("commit POSTs = %d, want exactly 1", commitCalls)
	}
	if refPatches != 1 {
		t.Errorf("ref PATCHes = %d, want exactly 1", refPatches)
	}
}

// --- SetVariable is safe to call twice ---------------------------------------

func TestSetVariable_SafeToCallTwice(t *testing.T) {
	var creates, updates int
	var exists bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/actions/variables":
			if exists {
				w.WriteHeader(http.StatusConflict)
				return
			}
			creates++
			exists = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/widget/actions/variables/MITSTACK_DISPATCH_URL":
			updates++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv, "test-token-abc123")

	if err := c.SetVariable(context.Background(), "acme/widget", "MITSTACK_DISPATCH_URL", "https://cicd.mitstack.dev"); err != nil {
		t.Fatalf("first SetVariable: %v", err)
	}
	if err := c.SetVariable(context.Background(), "acme/widget", "MITSTACK_DISPATCH_URL", "https://cicd.mitstack.dev/v2"); err != nil {
		t.Fatalf("second SetVariable: %v", err)
	}
	if creates != 1 {
		t.Errorf("create calls = %d, want 1", creates)
	}
	if updates != 1 {
		t.Errorf("update (PATCH) calls = %d, want exactly 1 — the repeat call must update, not fail", updates)
	}
}

// --- The installation token never leaks (AC#5) --------------------------------
//
// These drive a GENUINE failure path — the upstream response body contains
// the token — and assert it is absent from the returned error. A guard that
// merely returns before any HTTP call would not exercise this at all.

func TestNoTokenLeak_EnsureRepoUpstreamFailureBody(t *testing.T) {
	const token = "ghs_super-secret-installation-token-999"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/acme/repos":
			// The realistic hostile case: GitHub's error payload happens to
			// echo back the Authorization header it received.
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"bad credentials","auth":"` + token + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv, token)

	_, err := c.EnsureRepo(context.Background(), "acme", "widget")
	if err == nil {
		t.Fatal("expected the upstream 422 to surface as an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("EnsureRepo error leaked the installation token: %v", err)
	}
}

func TestNoTokenLeak_CommitFilesUpstreamFailureBody(t *testing.T) {
	const token = "ghs_super-secret-installation-token-999"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget/git/ref/heads/main":
			w.WriteHeader(http.StatusNotFound) // treat as a fresh, empty repo
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/git/trees":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"rejected for token ` + token + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv, token)

	err := c.CommitFiles(context.Background(), "acme/widget", "seed", map[string]string{"a.txt": "x"})
	if err == nil {
		t.Fatal("expected the upstream 422 to surface as an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("CommitFiles error leaked the installation token: %v", err)
	}
}

func TestNoTokenLeak_SetVariableUpstreamFailureBody(t *testing.T) {
	const token = "ghs_super-secret-installation-token-999"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"upstream failure","request_token":"` + token + `"}`))
	}))
	defer srv.Close()
	c := newTestClient(srv, token)

	err := c.SetVariable(context.Background(), "acme/widget", "X", "y")
	if err == nil {
		t.Fatal("expected the upstream 500 to surface as an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("SetVariable error leaked the installation token: %v", err)
	}
}
