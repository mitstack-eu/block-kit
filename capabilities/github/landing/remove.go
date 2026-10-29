package landing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/handler"
)

// Removal is what one subscription's unsubscribe removes: every file its records created that main still carries unchanged.
type Removal struct {
	Subscription string
	Records      []Record
}

// Kept is a file a removal left on main because the app changed it.
type Kept struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Reason     string `json:"reason"`
}

type Result struct {
	Kept []Kept
}

const keptChanged = "changed"

var subscriptionID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// BranchPrefix is the prefix of every branch a subscription's landings and its removal use, each deleted once the removal is done.
func BranchPrefix(subscription string) string {
	return "mitstack/" + subscription + "/"
}

// Remove opens one pull request per repository deleting every file r's records created that main still carries at its recorded blob, merges it once main's required checks pass on it, and deletes every branch under the subscription's prefix.
// While a pull request waits on a check, or a repository cannot be read, it returns a handler.Waiting and writes nothing it has not written before, so the unsubscribe stays unsettled and is re-driven.
func Remove(ctx context.Context, c *ghapi.Client, r Removal) (Result, error) {
	if !subscriptionID.MatchString(r.Subscription) {
		return Result{}, fmt.Errorf("landing: subscription %q is not a subscription id", r.Subscription)
	}
	var result Result
	var waits []string
	for _, rec := range byRepository(r.Records) {
		kept, err := removeFrom(ctx, c, rec, r.Subscription)
		var w *handler.Waiting
		if errors.As(err, &w) {
			waits = append(waits, w.Detail)
			continue
		}
		if err != nil {
			return Result{}, err
		}
		result.Kept = append(result.Kept, kept...)
	}
	if len(waits) > 0 {
		return Result{}, handler.Wait("%s", strings.Join(waits, "; "))
	}
	return result, nil
}

// byRepository joins records of one repository, keeping the first record of a path.
func byRepository(records []Record) []Record {
	var out []Record
	index := map[string]int{}
	seen := map[string]bool{}
	for _, rec := range records {
		i, ok := index[rec.Repository]
		if !ok {
			i = len(out)
			index[rec.Repository] = i
			out = append(out, Record{Repository: rec.Repository})
		}
		for _, f := range rec.Files {
			if !seen[rec.Repository+"\x00"+f.Path] {
				seen[rec.Repository+"\x00"+f.Path] = true
				out[i].Files = append(out[i].Files, f)
			}
		}
	}
	return out
}

func removeFrom(ctx context.Context, c *ghapi.Client, rec Record, subscription string) ([]Kept, error) {
	st, err := readMain(ctx, c, rec)
	if err != nil {
		return nil, handler.Wait("%s cannot be read, so nothing is removed from it yet: %v", rec.Repository, err)
	}
	if st.deleted {
		return nil, nil
	}
	kept := make([]Kept, 0, len(st.changed))
	for _, path := range st.changed {
		kept = append(kept, Kept{Repository: rec.Repository, Path: path, Reason: keptChanged})
	}
	a := api{c: c, repo: rec.Repository}
	if len(st.unchanged) > 0 {
		if err := a.removeThroughPullRequest(ctx, st, subscription, kept); err != nil {
			return nil, err
		}
	}
	if err := a.deleteBranches(ctx, subscription); err != nil {
		return nil, err
	}
	return kept, nil
}

func (a api) removeThroughPullRequest(ctx context.Context, st mainState, subscription string, kept []Kept) error {
	branch := BranchPrefix(subscription) + "remove"
	title := fmt.Sprintf("mitstack: remove the files subscription %s landed", subscription)
	commit, err := a.removalCommit(ctx, st, branch, title)
	if err != nil {
		return err
	}
	number, err := a.pullRequest(ctx, branch, title, removalBody(st.unchanged, kept))
	if err != nil {
		return err
	}
	if err := a.requiredChecksPassed(ctx, number, commit); err != nil {
		return err
	}
	return a.merge(ctx, number, commit)
}

// removalCommit returns the branch's commit when it already deletes from main's head, and otherwise builds one onto the head and points the branch at it.
func (a api) removalCommit(ctx context.Context, st mainState, branch, message string) (string, error) {
	current, err := a.branchCommit(ctx, branch)
	if err != nil {
		return "", err
	}
	if current != "" {
		var c struct {
			Parents []struct {
				SHA string `json:"sha"`
			} `json:"parents"`
		}
		if err := a.repos(ctx, http.MethodGet, "/git/commits/"+current, nil, &c); err != nil {
			return "", err
		}
		if len(c.Parents) == 1 && c.Parents[0].SHA == st.head {
			return current, nil
		}
	}
	baseTree, err := a.c.CommitTree(ctx, a.repo, st.head)
	if err != nil {
		return "", err
	}
	entries := make([]map[string]any, 0, len(st.unchanged))
	for _, path := range st.unchanged {
		entries = append(entries, map[string]any{"path": path, "mode": "100644", "type": "blob", "sha": nil})
	}
	var tree, commit struct {
		SHA string `json:"sha"`
	}
	if err := a.repos(ctx, http.MethodPost, "/git/trees", map[string]any{"base_tree": baseTree, "tree": entries}, &tree); err != nil {
		return "", err
	}
	if err := a.repos(ctx, http.MethodPost, "/git/commits", map[string]any{"message": message, "tree": tree.SHA, "parents": []string{st.head}}, &commit); err != nil {
		return "", err
	}
	if current == "" {
		return commit.SHA, a.repos(ctx, http.MethodPost, "/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": commit.SHA}, nil)
	}
	return commit.SHA, a.repos(ctx, http.MethodPatch, "/git/refs/heads/"+branch, map[string]any{"sha": commit.SHA, "force": true}, nil)
}

// branchCommit returns "" for a branch that does not exist.
func (a api) branchCommit(ctx context.Context, branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := a.repos(ctx, http.MethodGet, "/git/ref/heads/"+branch, nil, &ref)
	if statusOf(err) == http.StatusNotFound {
		return "", nil
	}
	return ref.Object.SHA, err
}

// pullRequest returns the open pull request from branch, opening one when none is.
func (a api) pullRequest(ctx context.Context, branch, title, body string) (int, error) {
	owner, _, _ := strings.Cut(a.repo, "/")
	query := url.Values{"head": {owner + ":" + branch}, "state": {"open"}}.Encode()
	var open []struct {
		Number int `json:"number"`
	}
	if err := a.repos(ctx, http.MethodGet, "/pulls?"+query, nil, &open); err != nil {
		return 0, err
	}
	if len(open) > 0 {
		return open[0].Number, nil
	}
	var opened struct {
		Number int `json:"number"`
	}
	err := a.repos(ctx, http.MethodPost, "/pulls", map[string]any{"title": title, "head": branch, "base": "main", "body": body}, &opened)
	return opened.Number, err
}

func removalBody(removed []string, kept []Kept) string {
	var b strings.Builder
	b.WriteString("This pull request removes the files a mitstack subscription landed in this repository that the app has not changed since:\n\n")
	for _, path := range removed {
		fmt.Fprintf(&b, "- `%s`\n", path)
	}
	if len(kept) > 0 {
		b.WriteString("\nThe app changed these files, so they stay:\n\n")
		for _, k := range kept {
			fmt.Fprintf(&b, "- `%s`\n", k.Path)
		}
	}
	return b.String()
}

// merge squashes the pull request at commit, and answers a merge GitHub refuses for now, 405 or 409, as waiting.
func (a api) merge(ctx context.Context, number int, commit string) error {
	err := a.repos(ctx, http.MethodPut, fmt.Sprintf("/pulls/%d/merge", number), map[string]any{"merge_method": "squash", "sha": commit}, nil)
	if s := statusOf(err); s == http.StatusMethodNotAllowed || s == http.StatusConflict {
		return handler.Wait("GitHub has not merged removal pull request #%d of %s yet: %v", number, a.repo, err)
	}
	return err
}

// deleteBranches deletes every branch under the subscription's prefix, a branch already gone included.
func (a api) deleteBranches(ctx context.Context, subscription string) error {
	prefix := BranchPrefix(subscription)
	var refs []struct {
		Ref string `json:"ref"`
	}
	if err := a.repos(ctx, http.MethodGet, "/git/matching-refs/heads/"+strings.TrimSuffix(prefix, "/"), nil, &refs); err != nil {
		return err
	}
	for _, r := range refs {
		name, ok := strings.CutPrefix(r.Ref, "refs/heads/")
		if !ok || !strings.HasPrefix(name, prefix) {
			continue
		}
		err := a.repos(ctx, http.MethodDelete, "/git/refs/heads/"+name, nil, nil)
		if err != nil && statusOf(err) != http.StatusUnprocessableEntity {
			return err
		}
	}
	return nil
}
