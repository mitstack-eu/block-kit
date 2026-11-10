package handler_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/gitops"
	"github.com/mitstack-eu/block-kit/capabilities/github/gitops/gitopstest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

// githubFake answers GitHub's routes from a repository and counts every request.
type githubFake struct {
	repo   *gitopstest.Repository
	mu     sync.Mutex
	routes map[string]int
	heads  atomic.Int64
	flaky  atomic.Int64
}

func (g *githubFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.routes[r.URL.Path]++
	g.mu.Unlock()
	path, last := r.URL.Path, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	switch {
	case strings.Contains(path, "/contents/"):
		content, ok, _ := g.repo.ReadFile(r.Context(), r.URL.Query().Get("ref"), last)
		switch {
		case last == "flaky.json" && g.flaky.Add(1) == 1:
			w.WriteHeader(http.StatusInternalServerError)
		case !ok:
			w.WriteHeader(http.StatusNotFound)
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString(content)})
		}
	case strings.Contains(path, "/git/ref/"):
		_, _ = fmt.Fprintf(w, `{"object":{"sha":"%040x"}}`, g.heads.Add(1))
	case strings.Contains(path, "/git/commits/"):
		_, _ = fmt.Fprintf(w, `{"tree":{"sha":%q}}`, last)
	default:
		g.repo.ServeGitHubCommit(w, r)
	}
}

func (g *githubFake) total() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, k := range g.routes {
		n += k
	}
	return n
}

// probe is an observer that runs its function, with the repository a validate would read.
type probe func(ctx context.Context) error

func (p probe) ObserveResource(ctx context.Context, _ contract.ResourceRef) (contract.Observation, error) {
	return contract.Observation{State: contract.Present}, p(ctx)
}

// reports answers no report on its first read and one listing the object web on every later read.
type reports struct{ calls atomic.Int64 }

var web = map[string]string{"kind": "deployment.apps", "name": "web", "namespace": "ns", "clusterSubscription": "cluster"}

func (c *reports) NodeReports(context.Context, contract.Desired, string) ([]observe.NodeReport, error) {
	if c.calls.Add(1) == 1 {
		return nil, nil
	}
	madeAt := time.Now().Add(time.Hour)
	body := fmt.Sprintf(`{"madeAt":%q,"subscriptionResources":[{"namespace":"ns","kind":"deployment.apps","name":"web","subscription":"sub-1"}],"subscriptionResourcesComplete":true}`, madeAt.Format(time.RFC3339))
	return []observe.NodeReport{{ReceivedAt: madeAt, Body: []byte(body)}}, nil
}

func (c *reports) observe(ctx context.Context, d contract.Desired) (contract.State, error) {
	obs, err := observe.NodeReported{API: c}.ObserveResource(ctx, contract.ResourceRef{Name: "r", Identity: web, Desired: d})
	return obs.State, err
}

// serveGitHub starts a fake GitHub holding a commit of held-0.json to held-{files-1}.json.
func serveGitHub(t *testing.T, files int) (*gitops.GitHubRepository, *githubFake, string) {
	t.Helper()
	repo, held := gitopstest.NewRepository(), map[string][]byte{}
	for i := range files {
		held[fmt.Sprintf("held-%d.json", i)] = []byte("{}")
	}
	revision, err := repo.Commit(context.Background(), "", held, "files")
	if err != nil {
		t.Fatal(err)
	}
	github := &githubFake{repo: repo, routes: map[string]int{}}
	srv := httptest.NewServer(github)
	t.Cleanup(srv.Close)
	return &gitops.GitHubRepository{Client: ghapi.NewClient(srv.URL, "token", nil), Repo: "acme/gitops"}, github, revision
}

func validateWith(t *testing.T, p probe) (int, contract.Response) {
	t.Helper()
	x := newHarness(t, &kittest.FakeResource{}, func(cfg *handler.Config) { cfg.Observers = map[string]handler.Observer{"r": p} })
	return x.answer(map[string]any{"operation": "validate"})
}

func TestValidate_ReadsEachFileOfACommitAndTheClusterReportsOnceHoweverOftenABlockAsks(t *testing.T) {
	for _, files := range []int{2, 8, 24} {
		t.Run(fmt.Sprintf("%d files", files), func(t *testing.T) {
			repo, github, revision := serveGitHub(t, files)
			cluster := &reports{}
			status, resp := validateWith(t, func(ctx context.Context) error {
				for range 5 {
					for i := range files {
						repo.ReadFile(ctx, revision, fmt.Sprintf("held-%d.json", i))
						repo.ReadFile(ctx, revision, fmt.Sprintf("missing-%d.json", i))
						cluster.observe(ctx, contract.Desired{})
					}
					repo.Changed(ctx, revision)
				}
				return nil
			})

			if status != http.StatusOK || resp.Error != "" {
				t.Fatalf("validate answered %d (%s)", status, resp.Error)
			}
			if got, want := github.total(), 3*files+1; got != want {
				t.Errorf("%d held and %d missing files read five times made %d GitHub requests (%v), want %d: a held file once, a missing file's read and its revision check once, the commit once", files, files, got, github.routes, want)
			}
			if got := cluster.calls.Load(); got != 1 {
				t.Errorf("the node reports were read %d times, want once", got)
			}
		})
	}
}

func TestValidate_KeepsNeitherTheHeadNorAFailedReadNorABranchName(t *testing.T) {
	repo, github, revision := serveGitHub(t, 1)
	var heads []string
	var failures []error
	var branchReads []error
	validateWith(t, func(ctx context.Context) error {
		for range 3 {
			head, _ := repo.Head(ctx)
			heads = append(heads, head)
		}
		for range 2 {
			_, _, err := repo.ReadFile(ctx, revision, "flaky.json")
			failures = append(failures, err)
		}
		for range 2 {
			_, _, err := repo.ReadFile(ctx, "main", "held-0.json")
			branchReads = append(branchReads, err)
		}
		return nil
	})

	if heads[0] == heads[1] || heads[1] == heads[2] {
		t.Errorf("three head reads gave %v, want three different heads as the branch moved", heads)
	}
	if failures[0] == nil || failures[1] != nil {
		t.Errorf("two reads of a file whose first read failed gave %v, want the failure and then the file", failures)
	}
	if branchReads[0] == nil || branchReads[1] == nil || github.routes["/repos/acme/gitops/contents/held-0.json"] != 0 {
		t.Errorf("reads of a branch name gave %v and reached GitHub %v, want a refusal each time before any request", branchReads, github.routes)
	}
}

func TestSubscribe_AWaitThatPollsStillSeesAReportThatArrivesDuringTheCall(t *testing.T) {
	cluster := &reports{}
	var states []contract.State
	x := newHarness(t, pollingBlock{func(ctx context.Context, d contract.Desired) error {
		for range 2 {
			state, err := cluster.observe(ctx, d)
			states = append(states, state)
			if err != nil {
				return err
			}
		}
		return nil
	}}, func(cfg *handler.Config) {
		cfg.Observers = map[string]handler.Observer{"r": probe(func(context.Context) error { return nil })}
	})

	x.post(map[string]any{"operation": "subscribe", "subscriptionId": "sub-1"})

	if len(states) != 2 || states[0] != contract.Unknown || states[1] != contract.Present {
		t.Errorf("two polls inside one subscribe read %v, want unknown and then present once the report arrived", states)
	}
}

type pollingBlock struct {
	ensure func(context.Context, contract.Desired) error
}

func (p pollingBlock) Ensure(ctx context.Context, d contract.Desired) error { return p.ensure(ctx, d) }

func (pollingBlock) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, nil
}

func TestValidate_ServesNoSubscriptionFromAnotherWhenTwoValidateAtOnce(t *testing.T) {
	repo, github, revision := serveGitHub(t, 2)
	cluster := &reports{}
	x := newHarness(t, &kittest.FakeResource{}, func(cfg *handler.Config) {
		cfg.Observers = map[string]handler.Observer{"r": probe(func(ctx context.Context) error {
			repo.ReadFile(ctx, revision, "held-0.json")
			repo.ReadFile(ctx, revision, "held-1.json")
			_, err := cluster.observe(ctx, contract.Desired{})
			return err
		})}
	})

	var wg sync.WaitGroup
	for _, sub := range []string{"sub-1", "sub-2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x.post(map[string]any{"operation": "validate", "subscriptionId": sub})
		}()
	}
	wg.Wait()

	if got := github.total(); got != 4 || cluster.calls.Load() != 2 {
		t.Errorf("two validates at once made %d GitHub requests and %d node-report reads, want 4 and 2: each reads for itself", got, cluster.calls.Load())
	}
}
