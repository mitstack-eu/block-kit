package gitopstest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/gitops"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

// Application is one Argo CD Application a node report holds, its Revision "" read as null, a revision Argo CD compared none at.
type Application struct {
	Name     string
	Revision string
	// RevisionLeftOut leaves the revision out, as a report of an earlier talos-cluster-kit release does.
	RevisionLeftOut bool
	// Objects nil leaves the list out, as a report cut to fit its size does.
	Objects []Object
}

// Object is one object an Application applied, its Health "" read as null, the health of a kind Argo CD runs no health check for.
type Object struct {
	gitops.Object
	Sync    string
	Health  string
	Message string
	// HealthLeftOut leaves the health out, as a report of an earlier talos-cluster-kit release does.
	HealthLeftOut bool
}

// Healthy is o applied, Synced and Healthy.
func Healthy(o gitops.Object) Object {
	return Object{Object: o, Sync: "Synced", Health: "Healthy"}
}

// NodeReports is an in-memory observe.NodeReportsAPI answering each cluster subscription's reports.
type NodeReports struct {
	mu      sync.Mutex
	reports map[string][]observe.NodeReport
	heads   map[string][]appliedHead
	err     error
}

type appliedHead struct {
	app  string
	repo *Repository
	set  []Object
}

func NewNodeReports() *NodeReports {
	return &NodeReports{reports: map[string][]observe.NodeReport{}, heads: map[string][]appliedHead{}}
}

// Report adds a report of cluster made at madeAt, holding apps.
func (r *NodeReports) Report(cluster string, madeAt time.Time, apps ...Application) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports[cluster] = append(r.reports[cluster], report(madeAt, apps))
}

// AppliesHead makes each read of cluster also answer a report made a minute after the request's verb, showing app at repo's head with each object head's files declare as set gives it, else Synced and Healthy.
func (r *NodeReports) AppliesHead(cluster, app string, repo *Repository, set ...Object) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.heads[cluster] = append(r.heads[cluster], appliedHead{app: app, repo: repo, set: set})
}

func (r *NodeReports) FailWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *NodeReports) NodeReports(_ context.Context, d contract.Desired, cluster string) ([]observe.NodeReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	out := append([]observe.NodeReport(nil), r.reports[cluster]...)
	if len(r.heads[cluster]) == 0 {
		return out, nil
	}
	apps := make([]Application, 0, len(r.heads[cluster]))
	for _, head := range r.heads[cluster] {
		app, err := head.repo.appliedAtHead(head.app, head.set)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return append(out, report(d.VerbAt.Add(time.Minute), apps)), nil
}

// appliedAtHead is app at head, listing each object a file at head declares as set gives it, else Synced and Healthy.
func (r *Repository) appliedAtHead(app string, set []Object) (Application, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	head := r.head()
	tree := r.trees[head]
	paths := make([]string, 0, len(tree))
	for path := range tree {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	applied := Application{Name: app, Revision: head, Objects: []Object{}}
	for _, path := range paths {
		declared, err := gitops.Objects(tree[path])
		if err != nil {
			return Application{}, fmt.Errorf("gitopstest: reading the objects %s declares at %s: %w", path, head, err)
		}
		for _, o := range declared {
			applied.Objects = append(applied.Objects, stateOf(o, set))
		}
	}
	return applied, nil
}

func stateOf(o gitops.Object, set []Object) Object {
	for _, s := range set {
		if s.Object == o {
			return s
		}
	}
	return Healthy(o)
}

func report(madeAt time.Time, apps []Application) observe.NodeReport {
	listed := make([]map[string]any, 0, len(apps))
	for _, a := range apps {
		listed = append(listed, a.body())
	}
	body, _ := json.Marshal(map[string]any{"madeAt": madeAt.Format(time.RFC3339Nano), "argocd": map[string]any{"applications": listed}})
	return observe.NodeReport{ReceivedAt: madeAt.Add(time.Second), Body: body}
}

// body is the Application as talos-cluster-kit's node report writes it.
func (a Application) body() map[string]any {
	out := map[string]any{"name": a.Name, "health": "Healthy", "sync": "Synced"}
	if !a.RevisionLeftOut {
		out["revision"] = nullable(a.Revision)
	}
	if a.Objects == nil {
		return out
	}
	resources := make([]map[string]any, 0, len(a.Objects))
	for _, o := range a.Objects {
		resource := map[string]any{"group": o.Group, "kind": o.Kind, "namespace": nullable(o.Namespace), "name": o.Name, "sync": o.Sync}
		if !o.HealthLeftOut {
			resource["health"], resource["message"] = nullable(o.Health), nullable(o.Message)
		}
		resources = append(resources, resource)
	}
	out["resources"] = resources
	return out
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
