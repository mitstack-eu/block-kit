package ghapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"

	"github.com/mitstack-eu/operator-kit/safe"
)

// Client is a GitHub REST API client. Base is an ALREADY-RESOLVED base URL —
// production callers build it via Base(endpoint, extraHosts...).String()
// (see base.go); by the time it reaches Client the trust decision has
// already been made once, so Client itself carries no host-trust logic and a
// test fixture can supply an httptest URL directly. Token is never a
// package-level global — each Client carries its own.
type Client struct {
	HTTP  *http.Client
	Base  string
	Token string
}

// NewClient builds a Client from an already-resolved base URL, an
// installation token, and an *http.Client (nil uses http.DefaultClient).
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	return &Client{HTTP: httpClient, Base: baseURL, Token: token}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) headers(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

// EnsureRepo idempotently creates a private repo `name` in org `org` — a GET
// that returns 200 is treated as already-existing (no create call); a 404
// creates it via POST /orgs/{org}/repos. Returns the repo's full name,
// preferring the response's full_name when the create call reports one.
func (c *Client) EnsureRepo(ctx context.Context, org, name string) (string, error) {
	fullName := org + "/" + name
	repo, err := SafeRepo(fullName)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/repos/"+repo, nil)
	if err != nil {
		return "", fmt.Errorf("ghapi: build request: %w", err)
	}
	c.headers(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("ghapi: github unreachable: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return fullName, nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return "", fmt.Errorf("ghapi: github GET /repos/%s returned %d", repo, resp.StatusCode)
	}

	cresp, err := c.doJSON(ctx, http.MethodPost, "/orgs/"+url.PathEscape(org)+"/repos", map[string]any{
		"name":      name,
		"private":   true,
		"auto_init": true,
	})
	if err != nil {
		return "", err
	}
	defer cresp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(cresp.Body, 8<<10))
	if cresp.StatusCode >= 300 {
		return "", fmt.Errorf("ghapi: github POST /orgs/%s/repos returned %d: %s", org, cresp.StatusCode, safe.LogSafe(string(raw), c.Token))
	}
	var out struct {
		FullName string `json:"full_name"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.FullName != "" {
		return out.FullName, nil
	}
	return fullName, nil
}

// DeleteRepo idempotently deletes org/name. A 404 (already gone) and a
// 200/204 are all success — the MIT-1302/MIT-1124 teardown contract. Any
// other status is a real failure.
func (c *Client) DeleteRepo(ctx context.Context, org, name string) error {
	repo, err := SafeRepo(org + "/" + name)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.Base+"/repos/"+repo, nil)
	if err != nil {
		return fmt.Errorf("ghapi: build request: %w", err)
	}
	c.headers(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("ghapi: github unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return fmt.Errorf("ghapi: github DELETE /repos/%s returned %d: %s", repo, resp.StatusCode, safe.LogSafe(string(raw), c.Token))
}

// CommitFiles commits files onto repoFullName's default branch (main) in a
// single atomic commit via the Git Data API. When a base commit already
// exists AND the posted tree resolves to the SAME tree sha as the base
// (GitHub returns the identical sha for byte-identical content), this is a
// TRUE no-op (MIT-1230): no commit is created and the ref is not moved.
func (c *Client) CommitFiles(ctx context.Context, repoFullName, message string, files map[string]string) error {
	repo, err := SafeRepo(repoFullName)
	if err != nil {
		return err
	}

	baseSHA, baseTreeSHA, haveBase, err := c.baseCommit(ctx, repo)
	if err != nil {
		return err
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	entries := make([]map[string]string, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, map[string]string{"path": p, "mode": "100644", "type": "blob", "content": files[p]})
	}
	treeReq := map[string]any{"tree": entries}
	if haveBase {
		treeReq["base_tree"] = baseTreeSHA
	}
	treeSHA, err := c.postForSHA(ctx, "/repos/"+repo+"/git/trees", treeReq)
	if err != nil {
		return err
	}
	if haveBase && treeSHA == baseTreeSHA {
		return nil
	}

	commitReq := map[string]any{"message": message, "tree": treeSHA}
	if haveBase {
		commitReq["parents"] = []string{baseSHA}
	}
	commitSHA, err := c.postForSHA(ctx, "/repos/"+repo+"/git/commits", commitReq)
	if err != nil {
		return err
	}
	if haveBase {
		return c.patch(ctx, "/repos/"+repo+"/git/refs/heads/main", map[string]any{"sha": commitSHA})
	}
	_, err = c.postForSHA(ctx, "/repos/"+repo+"/git/refs", map[string]any{"ref": "refs/heads/main", "sha": commitSHA})
	return err
}

// baseCommit returns the current main commit sha + its tree sha. haveBase is
// false for a fresh/empty repo (no heads/main ref yet).
func (c *Client) baseCommit(ctx context.Context, repo string) (commitSHA, treeSHA string, haveBase bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/repos/"+repo+"/git/ref/heads/main", nil)
	if err != nil {
		return "", "", false, fmt.Errorf("ghapi: build request: %w", err)
	}
	c.headers(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", "", false, fmt.Errorf("ghapi: github unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", "", false, fmt.Errorf("ghapi: github GET git/ref/heads/main returned %d: %s", resp.StatusCode, safe.LogSafe(string(raw), c.Token))
	}
	var refObj struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refObj); err != nil {
		return "", "", false, fmt.Errorf("ghapi: decode ref: %w", err)
	}

	cr, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/repos/"+repo+"/git/commits/"+url.PathEscape(refObj.Object.SHA), nil)
	if err != nil {
		return "", "", false, fmt.Errorf("ghapi: build request: %w", err)
	}
	c.headers(cr)
	cresp, err := c.httpClient().Do(cr)
	if err != nil {
		return "", "", false, fmt.Errorf("ghapi: github unreachable: %w", err)
	}
	defer cresp.Body.Close()
	if cresp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(cresp.Body, 4<<10))
		return "", "", false, fmt.Errorf("ghapi: github GET git/commits returned %d: %s", cresp.StatusCode, safe.LogSafe(string(raw), c.Token))
	}
	var cObj struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(cresp.Body).Decode(&cObj); err != nil {
		return "", "", false, fmt.Errorf("ghapi: decode commit: %w", err)
	}
	return refObj.Object.SHA, cObj.Tree.SHA, true, nil
}

// postForSHA POSTs payload to path and returns the response's top-level
// "sha" field (empty for endpoints that don't return one).
func (c *Client) postForSHA(ctx context.Context, path string, payload any) (string, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, path, payload)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("ghapi: github POST %s returned %d: %s", path, resp.StatusCode, safe.LogSafe(string(raw), c.Token))
	}
	var out struct {
		SHA string `json:"sha"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.SHA, nil
}

func (c *Client) patch(ctx context.Context, path string, payload any) error {
	resp, err := c.doJSON(ctx, http.MethodPatch, path, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("ghapi: github PATCH %s returned %d: %s", path, resp.StatusCode, safe.LogSafe(string(raw), c.Token))
	}
	return nil
}

// SetVariable idempotently sets (creates or updates) an Actions repo
// variable. A create (POST) that reports <300 is done; a 409 or 422 (GitHub
// reports the variable already exists — the Actions variables API is not
// itself idempotent) falls back to PATCH .../actions/variables/{name}.
// Any other status is an error.
func (c *Client) SetVariable(ctx context.Context, repoFullName, name, value string) error {
	repo, err := SafeRepo(repoFullName)
	if err != nil {
		return err
	}

	resp, err := c.doJSON(ctx, http.MethodPost, "/repos/"+repo+"/actions/variables", map[string]any{
		"name":  name,
		"value": value,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode != http.StatusConflict && resp.StatusCode != http.StatusUnprocessableEntity {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("ghapi: github POST /repos/%s/actions/variables returned %d: %s", repo, resp.StatusCode, safe.LogSafe(string(raw), c.Token))
	}

	return c.patch(ctx, "/repos/"+repo+"/actions/variables/"+name, map[string]any{
		"name":  name,
		"value": value,
	})
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ghapi: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("ghapi: build request: %w", err)
	}
	c.headers(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("ghapi: github call failed: %w", err)
	}
	return resp, nil
}
