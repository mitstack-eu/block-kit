package main

// main_test.go pins the CONTRACT for the `board-delivery` command: the CLI
// a push-to-main CI job calls to derive which board keys the push delivered
// and record them once. The seam under test is
// Run(args []string, stdout io.Writer) int, following the kit's own
// blockprov.Run idiom: the whole command is testable without a real
// process, and TRACKER_BASE_URL / TRACKER_API_TOKEN are read from the
// environment via t.Setenv, exactly as the workflow job will set them.
//
// The git half runs against a hermetic temporary repository built with
// os/exec (git init/commit/tag), plus a bare "origin" remote, so the
// board-synced anchor tag's push can be observed without any network
// access or dependence on the real operator-kit repository's history. The
// HTTP half runs against httptest.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// --- hermetic git fixture ------------------------------------------------

// testRepo is a temporary working copy plus a bare "origin" remote, so a
// test can both make commits and observe what the command under test pushed
// — the board-synced tag only persists across CI runs once it reaches the
// remote, since each run's checkout is thrown away.
type testRepo struct {
	Dir       string // working copy, passed to the command as --repo
	OriginDir string // bare remote
	SeedSHA   string // the repo's first commit, made by newTestRepo
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	origin := t.TempDir()
	dir := t.TempDir()
	runGit(t, origin, "init", "-q", "--bare")
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "remote", "add", "origin", origin)

	r := &testRepo{Dir: dir, OriginDir: origin}
	r.SeedSHA = r.commit(t, "seed", "")
	runGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/main")
	return r
}

// commit makes an empty commit (subject, optional body) and returns its sha.
func (r *testRepo) commit(t *testing.T, subject, body string) string {
	t.Helper()
	args := []string{"-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "--allow-empty", "-m", subject}
	if body != "" {
		args = append(args, "-m", body)
	}
	runGit(t, r.Dir, args...)
	return runGit(t, r.Dir, "rev-parse", "HEAD")
}

// setLocalTag simulates a checkout that already fetched a board-synced tag
// left by an earlier successful run.
func (r *testRepo) setLocalTag(t *testing.T, tag, sha string) {
	t.Helper()
	runGit(t, r.Dir, "tag", "-f", tag, sha)
}

func (r *testRepo) localTagSHA(t *testing.T, tag string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "-q", "--verify", "refs/tags/"+tag+"^{commit}")
	cmd.Dir = r.Dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (r *testRepo) originTagSHA(t *testing.T, tag string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "-q", "--verify", "refs/tags/"+tag+"^{commit}")
	cmd.Dir = r.OriginDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// --- HTTP fixture ---------------------------------------------------------

type capturedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

// newRecordingServer answers every request with status/body and records it,
// so a test can assert exactly how many requests arrived and what they
// carried.
func newRecordingServer(t *testing.T, status int, body string) (*httptest.Server, *[]capturedRequest) {
	t.Helper()
	var reqs []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		reqs = append(reqs, capturedRequest{Method: r.Method, Path: r.URL.Path, Headers: r.Header.Clone(), Body: raw})
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

type deliveredPayload struct {
	Delivery string   `json:"delivery"`
	Keys     []string `json:"keys"`
}

func decodeDelivered(t *testing.T, raw []byte) deliveredPayload {
	t.Helper()
	var p deliveredPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decoding delivered payload %s: %v", raw, err)
	}
	return p
}

func keysEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// --- CLI fixture -----------------------------------------------------------

// runBoardDelivery invokes Run with --repo/--before/--after flags and
// TRACKER_BASE_URL/TRACKER_API_TOKEN set exactly as the workflow job will
// set them.
func runBoardDelivery(t *testing.T, repoDir, before, after, baseURL, token string) (stdout string, exitCode int) {
	t.Helper()
	t.Setenv("TRACKER_BASE_URL", baseURL)
	t.Setenv("TRACKER_API_TOKEN", token)
	args := []string{"--repo", repoDir, "--after", after}
	if before != "" {
		args = append(args, "--before", before)
	}
	var buf bytes.Buffer
	exitCode = Run(args, &buf)
	return buf.String(), exitCode
}

// --- the HTTP half ----------------------------------------------------------

func TestRun_PostsExactlyOneRequestWithDecodedJSONBody(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "MIT-42: fix the thing", "")
	srv, reqs := newRecordingServer(t, 200, "ok")

	_, code := runBoardDelivery(t, repo.Dir, "", sha, srv.URL, "s3cr3t-token")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	if len(*reqs) != 1 {
		t.Fatalf("server received %d requests, want exactly 1", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.Path != "/v1/delivered" {
		t.Errorf("path = %q, want /v1/delivered", req.Path)
	}
	if ct := req.Headers.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if auth := req.Headers.Get("Authorization"); auth != "Bearer s3cr3t-token" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer s3cr3t-token")
	}
	payload := decodeDelivered(t, req.Body)
	if payload.Delivery != sha {
		t.Errorf("delivery = %q, want %q", payload.Delivery, sha)
	}
	if !keysEqual(payload.Keys, []string{"MIT-42"}) {
		t.Errorf("keys = %v, want [MIT-42]", payload.Keys)
	}
}

func TestRun_OmitsAuthorizationHeaderWhenTokenEmpty(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "MIT-1: fix", "")
	srv, reqs := newRecordingServer(t, 200, "ok")

	_, code := runBoardDelivery(t, repo.Dir, "", sha, srv.URL, "")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	if len(*reqs) != 1 {
		t.Fatalf("server received %d requests, want exactly 1", len(*reqs))
	}
	if vals := (*reqs)[0].Headers.Values("Authorization"); len(vals) != 0 {
		t.Errorf("Authorization header = %v, want no header at all when the token is empty", vals)
	}
}

func TestRun_NoKeys_RecordsNothingAndSucceeds(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "chore: bump platform submodule to abc (MIT-1092 automation)", "")
	srv, reqs := newRecordingServer(t, 200, "ok")

	out, code := runBoardDelivery(t, repo.Dir, "", sha, srv.URL, "")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0 — a docs/automation-only push must not fail the run", code)
	}
	if len(*reqs) != 0 {
		t.Fatalf("server received %d requests, want 0 — no keys means no POST", len(*reqs))
	}
	if !strings.Contains(strings.ToLower(out), "recorded nothing") {
		t.Errorf("output = %q, want it to say it recorded nothing", out)
	}
}

func TestRun_EmptyBaseURL_WithKeys_IsHardFailure(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "MIT-1: fix", "")

	out, code := runBoardDelivery(t, repo.Dir, "", sha, "", "")
	if code == 0 {
		t.Fatalf("Run exit code = 0, want non-zero — an unset TRACKER_BASE_URL must never silently skip a real delivery")
	}
	if !strings.Contains(strings.ToLower(out), "no board to record on") {
		t.Errorf("output = %q, want it to say the delivery has no board to record on", out)
	}
}

func TestRun_EmptyBaseURL_NoKeys_Succeeds(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "chore: bump platform submodule to abc (MIT-1092 automation)", "")

	out, code := runBoardDelivery(t, repo.Dir, "", sha, "", "")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0 — nothing was ever going to be recorded, so a missing board is moot", code)
	}
	if !strings.Contains(strings.ToLower(out), "recorded nothing") {
		t.Errorf("output = %q, want it to say it recorded nothing", out)
	}
}

func TestRun_NonSuccessStatus_IsHardFailureIncludingStatusAndBody(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "MIT-1: fix", "")
	srv, _ := newRecordingServer(t, 500, "boom-details")

	out, code := runBoardDelivery(t, repo.Dir, "", sha, srv.URL, "")
	if code == 0 {
		t.Fatalf("Run exit code = 0, want non-zero on a rejected record")
	}
	if !strings.Contains(out, "500") {
		t.Errorf("output = %q, want it to include the response status code 500", out)
	}
	if !strings.Contains(out, "boom-details") {
		t.Errorf("output = %q, want it to include the response body", out)
	}
}

func TestRun_TransportError_IsHardFailure(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "MIT-1: fix", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	baseURL := srv.URL
	srv.Close() // nothing is listening by the time Run dials it

	_, code := runBoardDelivery(t, repo.Dir, "", sha, baseURL, "")
	if code == 0 {
		t.Fatalf("Run exit code = 0, want non-zero on a transport failure")
	}
}

func TestRun_TokenNeverAppearsInErrorTextOrOutput(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "MIT-1: fix", "")
	const token = "super-secret-value-xyz"
	srv, reqs := newRecordingServer(t, 401, "unauthorized")

	out, code := runBoardDelivery(t, repo.Dir, "", sha, srv.URL, token)
	if code == 0 {
		t.Fatalf("Run exit code = 0, want non-zero on a 401")
	}
	if strings.Contains(out, token) {
		t.Fatalf("output contains the bearer token verbatim: %q", out)
	}
	if len(*reqs) != 1 {
		t.Fatalf("server received %d requests, want exactly 1", len(*reqs))
	}
	if auth := (*reqs)[0].Headers.Get("Authorization"); auth != "Bearer "+token {
		t.Errorf("Authorization sent to the server = %q, want %q — the token must reach the request even though it never reaches output", auth, "Bearer "+token)
	}
}

// --- the git half: range resolution wired to real commits -----------------

func TestRun_RangeExcludesTheBeforeCommitItself(t *testing.T) {
	repo := newTestRepo(t)
	shaA := repo.commit(t, "MIT-1: one", "")
	repo.commit(t, "MIT-2: two", "")
	shaC := repo.commit(t, "MIT-3: three", "")
	srv, reqs := newRecordingServer(t, 200, "ok")

	_, code := runBoardDelivery(t, repo.Dir, shaA, shaC, srv.URL, "")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	payload := decodeDelivered(t, (*reqs)[0].Body)
	if !keysEqual(payload.Keys, []string{"MIT-2", "MIT-3"}) {
		t.Errorf("keys = %v, want [MIT-2 MIT-3] — MIT-1's commit (before) must be excluded", payload.Keys)
	}
}

func TestRun_UnknownBeforeAndNoAnchorUsesOnlyTheTipCommit(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit(t, "MIT-1: one", "")
	shaB := repo.commit(t, "MIT-2: two", "")
	srv, reqs := newRecordingServer(t, 200, "ok")

	_, code := runBoardDelivery(t, repo.Dir, "", shaB, srv.URL, "")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	payload := decodeDelivered(t, (*reqs)[0].Body)
	if !keysEqual(payload.Keys, []string{"MIT-2"}) {
		t.Errorf("keys = %v, want [MIT-2] only — an unresolvable before must fall back to the single tip commit, never full history", payload.Keys)
	}
}

func TestRun_ZeroSHABeforeAndNoAnchorUsesOnlyTheTipCommit(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit(t, "MIT-1: one", "")
	shaB := repo.commit(t, "MIT-2: two", "")
	srv, reqs := newRecordingServer(t, 200, "ok")

	_, code := runBoardDelivery(t, repo.Dir, testZeroSHA, shaB, srv.URL, "")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	payload := decodeDelivered(t, (*reqs)[0].Body)
	if !keysEqual(payload.Keys, []string{"MIT-2"}) {
		t.Errorf("keys = %v, want [MIT-2] only — GitHub's all-zero before sentinel must fall back to the tip commit", payload.Keys)
	}
}

func TestRun_MergesAndSortsKeysNumericallyAcrossCommits(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit(t, "MIT-20: two", "")
	shaB := repo.commit(t, "MIT-9: nine", "")
	srv, reqs := newRecordingServer(t, 200, "ok")

	_, code := runBoardDelivery(t, repo.Dir, repo.SeedSHA, shaB, srv.URL, "")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	payload := decodeDelivered(t, (*reqs)[0].Body)
	if !keysEqual(payload.Keys, []string{"MIT-9", "MIT-20"}) {
		t.Errorf("keys = %v, want [MIT-9 MIT-20] — numeric order across commits, not appearance order or lexical order", payload.Keys)
	}
}

func TestRun_DedupesKeyAppearingInSubjectAndTrailerOfOneCommit(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "MIT-5: fix", "Jira: MIT-5 MIT-6")
	srv, reqs := newRecordingServer(t, 200, "ok")

	_, code := runBoardDelivery(t, repo.Dir, "", sha, srv.URL, "")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	payload := decodeDelivered(t, (*reqs)[0].Body)
	if !keysEqual(payload.Keys, []string{"MIT-5", "MIT-6"}) {
		t.Errorf("keys = %v, want [MIT-5 MIT-6] deduped", payload.Keys)
	}
}

// --- the board-synced anchor tag --------------------------------------------

func TestRun_UsesAnchorTagOverBeforeFlagWhenTagIsPresent(t *testing.T) {
	repo := newTestRepo(t)
	shaA := repo.commit(t, "MIT-1: one", "")
	repo.commit(t, "MIT-2: two", "")
	shaC := repo.commit(t, "MIT-3: three", "")
	repo.setLocalTag(t, "board-synced", shaA)
	srv, reqs := newRecordingServer(t, 200, "ok")

	// --before points at the seed commit, which would (wrongly) include
	// shaA's MIT-1 if the anchor tag were not preferred.
	_, code := runBoardDelivery(t, repo.Dir, repo.SeedSHA, shaC, srv.URL, "")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	payload := decodeDelivered(t, (*reqs)[0].Body)
	if !keysEqual(payload.Keys, []string{"MIT-2", "MIT-3"}) {
		t.Errorf("keys = %v, want [MIT-2 MIT-3] — the board-synced anchor tag must win over --before", payload.Keys)
	}
}

func TestRun_AdvancesAndPushesAnchorTagAfterASuccessfulWrite(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "MIT-1: one", "")
	srv, _ := newRecordingServer(t, 200, "ok")

	_, code := runBoardDelivery(t, repo.Dir, "", sha, srv.URL, "")
	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	if got := repo.localTagSHA(t, "board-synced"); got != sha {
		t.Errorf("local board-synced tag = %q, want it advanced to %q", got, sha)
	}
	if got := repo.originTagSHA(t, "board-synced"); got != sha {
		t.Errorf("origin's board-synced tag = %q, want it pushed to %q — a run's own checkout is discarded, so the anchor only survives if it reaches the remote", got, sha)
	}
}

func TestRun_DoesNotAdvanceAnchorTagWhenTheWriteFails(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "MIT-1: one", "")
	srv, _ := newRecordingServer(t, 500, "boom")

	_, code := runBoardDelivery(t, repo.Dir, "", sha, srv.URL, "")
	if code == 0 {
		t.Fatalf("Run exit code = 0, want non-zero on a rejected record")
	}
	if got := repo.localTagSHA(t, "board-synced"); got != "" {
		t.Errorf("local board-synced tag = %q, want it left unset — a failed write must not advance the anchor", got)
	}
	if got := repo.originTagSHA(t, "board-synced"); got != "" {
		t.Errorf("origin's board-synced tag = %q, want it left unset — a failed write must not advance the anchor", got)
	}
}

func TestRun_DoesNotAdvanceAnchorTagWhenBaseURLIsEmpty(t *testing.T) {
	repo := newTestRepo(t)
	sha := repo.commit(t, "MIT-1: one", "")

	_, code := runBoardDelivery(t, repo.Dir, "", sha, "", "")
	if code == 0 {
		t.Fatalf("Run exit code = 0, want non-zero — no board configured is a hard failure")
	}
	if got := repo.localTagSHA(t, "board-synced"); got != "" {
		t.Errorf("local board-synced tag = %q, want it left unset — a run that never wrote must not claim it synced", got)
	}
}
