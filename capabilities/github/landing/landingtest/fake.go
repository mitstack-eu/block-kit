// Package landingtest is an in-memory fake of the GitHub REST routes the landing package calls, holding one repository's main, branches, pull requests and checks.
package landingtest

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Request is one call the fake received, with the Authorization header it carried.
type Request struct {
	Method, Path, Authorization string
}

// Pull is a pull request as the fake holds it.
type Pull struct {
	Number         int
	Branch, Body   string
	Open, Merged   bool
	MergedRevision string
}

type commit struct {
	tree   map[string]string
	parent string
}

// Fake serves one repository, whose owner the installation covers whole unless CoverSelectedRepositories says otherwise.
type Fake struct {
	*httptest.Server
	Repo string

	mu        sync.Mutex
	commits   map[string]commit
	trees     map[string]map[string]string
	refs      map[string]string
	pulls     []*Pull
	required  []string
	ruled     []string
	checks    map[string]string
	selection string
	deleted   bool
	answers   map[string]int
	requests  []Request
	before    func(method, path string) error
}

func New(t testing.TB, repo string) *Fake {
	f := &Fake{
		Repo:      repo,
		commits:   map[string]commit{},
		trees:     map[string]map[string]string{},
		refs:      map[string]string{},
		checks:    map[string]string{},
		selection: "all",
		answers:   map[string]int{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// Seed commits files straight onto main, as the app or an earlier landing did.
func (f *Fake) Seed(files map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tree := maps.Clone(f.commits[f.refs["heads/main"]].tree)
	if tree == nil {
		tree = map[string]string{}
	}
	maps.Copy(tree, files)
	f.refs["heads/main"] = f.commit(f.storeTree(tree), f.refs["heads/main"])
}

// Remove commits main without paths, as another writer deleting them does.
func (f *Fake) Remove(paths ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tree := maps.Clone(f.commits[f.refs["heads/main"]].tree)
	for _, p := range paths {
		delete(tree, p)
	}
	f.refs["heads/main"] = f.commit(f.storeTree(tree), f.refs["heads/main"])
}

// Head returns the commit main points at, "" before the first Seed.
func (f *Fake) Head() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refs["heads/main"]
}

// BeforeWrite runs hook ahead of each request but a GET, outside the fake's lock so it may change main, and answers 422
// to a request it returns an error for.
func (f *Fake) BeforeWrite(hook func(method, path string) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.before = hook
}

// Edit changes one file on main, as the app does after a landing.
func (f *Fake) Edit(path, content string) { f.Seed(map[string]string{path: content}) }

// Branch creates a branch at main's head.
func (f *Fake) Branch(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs["heads/"+name] = f.refs["heads/main"]
}

// Main returns every file main carries.
func (f *Fake) Main() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.commits[f.refs["heads/main"]].tree)
}

// Branches lists every branch but main.
func (f *Fake) Branches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for ref := range f.refs {
		if name, ok := strings.CutPrefix(ref, "heads/"); ok && name != "main" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// ClosePull closes pull request number unmerged, as a person on GitHub can.
func (f *Fake) ClosePull(number int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls[number-1].Open = false
}

func (f *Fake) Pulls() []Pull {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Pull, 0, len(f.pulls))
	for _, p := range f.pulls {
		out = append(out, *p)
	}
	return out
}

// RequireCheck protects main: no ref update moves it, and a pull request merges only once the check reports success.
func (f *Fake) RequireCheck(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.required = append(f.required, name)
}

// RequireCheckByRuleset makes a ruleset require the check on main, which the branch's protection does not list.
func (f *Fake) RequireCheckByRuleset(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ruled = append(f.ruled, name)
}

// ReportCheck reports the check with conclusion on every commit.
func (f *Fake) ReportCheck(name, conclusion string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks[name] = conclusion
}

// DeleteRepository makes every route under the repository answer 404, as GitHub does for a deleted repository.
func (f *Fake) DeleteRepository() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = true
}

// Deleted reports whether DeleteRepository was called, as a block deleting the repository whole does.
func (f *Fake) Deleted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleted
}

// CoverSelectedRepositories makes the installation listing answer repository_selection "selected", as a token minted for chosen repositories does.
func (f *Fake) CoverSelectedRepositories() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selection = "selected"
}

// Answer makes every method request to path, its query left out, answer status with no body.
func (f *Fake) Answer(method, path string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[method+" "+path] = status
}

func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Writes lists every request but a GET.
func (f *Fake) Writes() []Request {
	var out []Request
	for _, r := range f.Requests() {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	before := f.before
	f.mu.Unlock()
	if before != nil && r.Method != http.MethodGet {
		if err := before(r.Method, r.URL.Path); err != nil {
			reply(w, http.StatusUnprocessableEntity, map[string]any{"message": err.Error()})
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.RequestURI(), Authorization: r.Header.Get("Authorization")})
	if status, ok := f.answers[r.Method+" "+r.URL.Path]; ok {
		w.WriteHeader(status)
		return
	}
	if r.URL.Path == "/installation/repositories" {
		owner, _, _ := strings.Cut(f.Repo, "/")
		reply(w, http.StatusOK, map[string]any{"repository_selection": f.selection, "repositories": []any{map[string]any{"owner": map[string]any{"login": owner}}}})
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/repos/"+f.Repo)
	if !ok || f.deleted {
		reply(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	status, out := f.route(r.Method, rest, r, body)
	reply(w, status, out)
}

func (f *Fake) route(method, rest string, r *http.Request, body map[string]any) (int, any) {
	switch {
	case method == http.MethodGet && rest == "":
		return http.StatusOK, map[string]any{"full_name": f.Repo}
	case method == http.MethodGet && strings.HasPrefix(rest, "/git/ref/"):
		return f.readRef(strings.TrimPrefix(rest, "/git/ref/"))
	case method == http.MethodGet && strings.HasPrefix(rest, "/git/matching-refs/"):
		return f.matchingRefs(strings.TrimPrefix(rest, "/git/matching-refs/"))
	case method == http.MethodGet && strings.HasPrefix(rest, "/git/commits/"):
		return f.readCommit(strings.TrimPrefix(rest, "/git/commits/"))
	case method == http.MethodGet && strings.HasPrefix(rest, "/git/trees/"):
		return f.listTree(strings.TrimPrefix(rest, "/git/trees/"))
	case method == http.MethodGet && strings.HasPrefix(rest, "/contents/"):
		return f.readContents(strings.TrimPrefix(rest, "/contents/"), r.URL.Query().Get("ref"))
	case method == http.MethodPost && rest == "/git/trees":
		return f.createTree(body)
	case method == http.MethodPost && rest == "/git/commits":
		return f.createCommit(body)
	case method == http.MethodPost && rest == "/git/refs":
		return f.createRef(body)
	case method == http.MethodPatch && strings.HasPrefix(rest, "/git/refs/"):
		return f.updateRef(strings.TrimPrefix(rest, "/git/refs/"), body)
	case method == http.MethodDelete && strings.HasPrefix(rest, "/git/refs/"):
		return f.deleteRef(strings.TrimPrefix(rest, "/git/refs/"))
	case method == http.MethodGet && rest == "/pulls":
		return f.listPulls(r.URL.Query().Get("head"), r.URL.Query().Get("state"))
	case method == http.MethodPost && rest == "/pulls":
		return f.openPull(body)
	case method == http.MethodPut && strings.HasPrefix(rest, "/pulls/") && strings.HasSuffix(rest, "/merge"):
		return f.mergePull(strings.TrimSuffix(strings.TrimPrefix(rest, "/pulls/"), "/merge"), body)
	case method == http.MethodGet && rest == "/branches/main":
		return f.readBranch()
	case method == http.MethodGet && rest == "/rules/branches/main":
		return f.readRules()
	case method == http.MethodGet && strings.HasPrefix(rest, "/commits/") && strings.HasSuffix(rest, "/check-runs"):
		return f.checkRuns()
	case method == http.MethodGet && strings.HasPrefix(rest, "/commits/") && strings.HasSuffix(rest, "/status"):
		return http.StatusOK, map[string]any{"state": "pending", "statuses": []any{}}
	}
	return http.StatusTeapot, map[string]any{"message": "landingtest serves no " + method + " " + rest}
}

func (f *Fake) readRef(ref string) (int, any) {
	sha, ok := f.refs[ref]
	if !ok {
		return http.StatusNotFound, map[string]any{"message": "Not Found"}
	}
	return http.StatusOK, refBody(ref, sha)
}

func (f *Fake) matchingRefs(prefix string) (int, any) {
	out := []any{}
	for _, ref := range slices.Sorted(maps.Keys(f.refs)) {
		if strings.HasPrefix(ref, prefix) {
			out = append(out, refBody(ref, f.refs[ref]))
		}
	}
	return http.StatusOK, out
}

func (f *Fake) readCommit(sha string) (int, any) {
	c, ok := f.commits[sha]
	if !ok {
		return http.StatusNotFound, map[string]any{"message": "No commit found for SHA: " + sha}
	}
	parents := []any{}
	if c.parent != "" {
		parents = append(parents, map[string]any{"sha": c.parent})
	}
	return http.StatusOK, map[string]any{"sha": sha, "tree": map[string]any{"sha": f.treeID(c.tree)}, "parents": parents}
}

// listTree lists a tree's every file, as GitHub's recursive listing does, holding no directory entry.
func (f *Fake) listTree(id string) (int, any) {
	tree, ok := f.trees[id]
	if !ok {
		return http.StatusNotFound, map[string]any{"message": "Not Found"}
	}
	entries := []any{}
	for _, path := range slices.Sorted(maps.Keys(tree)) {
		entries = append(entries, map[string]any{"path": path, "type": "blob", "sha": blob(tree[path])})
	}
	return http.StatusOK, map[string]any{"sha": id, "truncated": false, "tree": entries}
}

func (f *Fake) readContents(path, ref string) (int, any) {
	content, ok := f.commits[ref].tree[path]
	if !ok {
		return http.StatusNotFound, map[string]any{"message": "Not Found"}
	}
	return http.StatusOK, map[string]any{"type": "file", "encoding": "base64", "path": path, "sha": blob(content), "content": base64.StdEncoding.EncodeToString([]byte(content))}
}

func (f *Fake) createTree(body map[string]any) (int, any) {
	tree := maps.Clone(f.trees[str(body["base_tree"])])
	if tree == nil {
		tree = map[string]string{}
	}
	entries, _ := body["tree"].([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		content, has := entry["content"].(string)
		if !has {
			delete(tree, str(entry["path"]))
			continue
		}
		tree[str(entry["path"])] = content
	}
	return http.StatusCreated, map[string]any{"sha": f.storeTree(tree)}
}

func (f *Fake) createCommit(body map[string]any) (int, any) {
	parents, _ := body["parents"].([]any)
	parent := ""
	if len(parents) > 0 {
		parent = str(parents[0])
	}
	return http.StatusCreated, map[string]any{"sha": f.commit(str(body["tree"]), parent)}
}

func (f *Fake) createRef(body map[string]any) (int, any) {
	ref := strings.TrimPrefix(str(body["ref"]), "refs/")
	if _, ok := f.refs[ref]; ok {
		return http.StatusUnprocessableEntity, map[string]any{"message": "Reference already exists"}
	}
	if ref == "heads/main" && f.protected() {
		return http.StatusUnprocessableEntity, map[string]any{"message": "Protected branch update failed"}
	}
	f.refs[ref] = str(body["sha"])
	return http.StatusCreated, refBody(ref, f.refs[ref])
}

func (f *Fake) updateRef(ref string, body map[string]any) (int, any) {
	current, ok := f.refs[ref]
	if !ok {
		return http.StatusUnprocessableEntity, map[string]any{"message": "Reference does not exist"}
	}
	if ref == "heads/main" && f.protected() {
		return http.StatusUnprocessableEntity, map[string]any{"message": "Protected branch update failed for refs/heads/main."}
	}
	next := str(body["sha"])
	if force, _ := body["force"].(bool); !force && f.commits[next].parent != current {
		return http.StatusUnprocessableEntity, map[string]any{"message": "Update is not a fast forward"}
	}
	f.refs[ref] = next
	return http.StatusOK, refBody(ref, next)
}

func (f *Fake) deleteRef(ref string) (int, any) {
	if _, ok := f.refs[ref]; !ok || ref == "heads/main" {
		return http.StatusUnprocessableEntity, map[string]any{"message": "Reference does not exist"}
	}
	delete(f.refs, ref)
	for _, p := range f.pulls {
		if "heads/"+p.Branch == ref {
			p.Open = false
		}
	}
	return http.StatusNoContent, nil
}

func (f *Fake) listPulls(head, state string) (int, any) {
	_, branch, _ := strings.Cut(head, ":")
	out := []any{}
	for _, p := range f.pulls {
		if p.Branch == branch && (state != "open" || p.Open) {
			out = append(out, f.pullBody(p))
		}
	}
	return http.StatusOK, out
}

func (f *Fake) openPull(body map[string]any) (int, any) {
	branch := str(body["head"])
	if _, ok := f.refs["heads/"+branch]; !ok || str(body["base"]) != "main" {
		return http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed"}
	}
	p := &Pull{Number: len(f.pulls) + 1, Branch: branch, Body: str(body["body"]), Open: true}
	f.pulls = append(f.pulls, p)
	return http.StatusCreated, f.pullBody(p)
}

func (f *Fake) mergePull(number string, body map[string]any) (int, any) {
	n, _ := strconv.Atoi(number)
	if n < 1 || n > len(f.pulls) || !f.pulls[n-1].Open {
		return http.StatusNotFound, map[string]any{"message": "Not Found"}
	}
	p := f.pulls[n-1]
	head := f.refs["heads/"+p.Branch]
	if sha := str(body["sha"]); sha != "" && sha != head {
		return http.StatusConflict, map[string]any{"message": "Head branch was modified. Review and try the merge again."}
	}
	for _, check := range append(slices.Clone(f.required), f.ruled...) {
		if !passing(f.checks[check]) {
			return http.StatusMethodNotAllowed, map[string]any{"message": fmt.Sprintf("Required status check %q is expected.", check)}
		}
	}
	tree, ok := f.squash(head)
	if !ok {
		return http.StatusMethodNotAllowed, map[string]any{"message": "Pull Request is not mergeable"}
	}
	merged := f.commit(f.storeTree(tree), f.refs["heads/main"])
	f.refs["heads/main"] = merged
	p.Open, p.Merged, p.MergedRevision = false, true, merged
	return http.StatusOK, map[string]any{"sha": merged, "merged": true, "message": "Pull Request successfully merged"}
}

// squash applies what head changed against its parent onto main, refusing a path main has changed since that parent.
func (f *Fake) squash(head string) (map[string]string, bool) {
	changed := f.commits[head].tree
	base := f.commits[f.commits[head].parent].tree
	tree := maps.Clone(f.commits[f.refs["heads/main"]].tree)
	for path, was := range base {
		if _, kept := changed[path]; kept {
			continue
		}
		if tree[path] != was {
			return nil, false
		}
		delete(tree, path)
	}
	for path, content := range changed {
		if base[path] != content {
			tree[path] = content
		}
	}
	return tree, true
}

func (f *Fake) readBranch() (int, any) {
	checks := []any{}
	for _, c := range f.required {
		checks = append(checks, map[string]any{"context": c, "app_id": nil})
	}
	protection := map[string]any{"enabled": len(f.required) > 0, "required_status_checks": map[string]any{"enforcement_level": "everyone", "contexts": slices.Clone(f.required), "checks": checks}}
	return http.StatusOK, map[string]any{"name": "main", "protected": len(f.required) > 0, "protection": protection}
}

func (f *Fake) readRules() (int, any) {
	rules := []any{}
	for _, c := range f.ruled {
		rules = append(rules, map[string]any{"type": "required_status_checks", "ruleset_id": 1, "parameters": map[string]any{"required_status_checks": []any{map[string]any{"context": c}}}})
	}
	return http.StatusOK, rules
}

func (f *Fake) protected() bool { return len(f.required)+len(f.ruled) > 0 }

func (f *Fake) checkRuns() (int, any) {
	runs := []any{}
	for _, name := range slices.Sorted(maps.Keys(f.checks)) {
		runs = append(runs, map[string]any{"name": name, "status": "completed", "conclusion": f.checks[name]})
	}
	return http.StatusOK, map[string]any{"total_count": len(runs), "check_runs": runs}
}

func (f *Fake) pullBody(p *Pull) map[string]any {
	return map[string]any{"number": p.Number, "state": map[bool]string{true: "open", false: "closed"}[p.Open], "head": map[string]any{"ref": p.Branch, "sha": f.refs["heads/"+p.Branch]}}
}

func (f *Fake) storeTree(tree map[string]string) string {
	id := f.treeID(tree)
	f.trees[id] = maps.Clone(tree)
	return id
}

func (f *Fake) treeID(tree map[string]string) string {
	h := sha1.New()
	for _, path := range slices.Sorted(maps.Keys(tree)) {
		fmt.Fprintf(h, "%s\x00%s\x00", path, blob(tree[path]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (f *Fake) commit(treeID, parent string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s %s %d", treeID, parent, len(f.commits))))
	sha := hex.EncodeToString(sum[:])
	f.commits[sha] = commit{tree: maps.Clone(f.trees[treeID]), parent: parent}
	return sha
}

func refBody(ref, sha string) map[string]any {
	return map[string]any{"ref": "refs/" + ref, "object": map[string]any{"type": "commit", "sha": sha}}
}

func passing(conclusion string) bool {
	return conclusion == "success" || conclusion == "neutral" || conclusion == "skipped"
}

func blob(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return hex.EncodeToString(sum[:])
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}
