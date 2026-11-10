package gitopstest

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/gitops"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

// Application is one Argo CD Application a node report holds, its Revision "" written as null, as the report holds before Argo CD's first comparison and for an Application with several sources.
type Application struct {
	Name     string
	Revision string
	// RevisionLeftOut leaves the revision out, as a report of an earlier talos-cluster-kit release does.
	RevisionLeftOut bool
	// Objects nil leaves the list out, as a report cut to fit its size does.
	Objects []Object
}

// Object is one object an Application applied, its Health "" written as null, the health of a kind Argo CD runs no health check for, and its Message written only beside a health neither Healthy nor null, as the report writes it.
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
	reports map[string][]nodeReport
	views   map[string][]Applied
	err     error
}

// Applied is an Argo CD Application a node report shows, read from Repo's files when the report is read.
type Applied struct {
	Name string
	Repo *Repository
	// Revision is the revision shown, whose files declare the objects listed, and "" reads Repo's head.
	Revision string
	// Paths names the files whose objects are listed, in that order, as a Deployment names the block's, and nil lists every file's.
	Paths []string
	// Set gives each object it names its state, and an object it names that no file declares follows the rest, as a sibling's or one Argo CD has yet to prune.
	Set []Object
}

type nodeReport struct {
	madeAt time.Time
	argocd json.RawMessage
}

func NewNodeReports() *NodeReports {
	return &NodeReports{reports: map[string][]nodeReport{}, views: map[string][]Applied{}}
}

// Report adds a report of cluster made at madeAt, holding apps.
func (r *NodeReports) Report(cluster string, madeAt time.Time, apps ...Application) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports[cluster] = append(r.reports[cluster], reportOf(madeAt, apps))
}

// Applies makes each read of cluster also answer a report made a minute after the request's verb showing a, in place of the Application of its name an earlier call showed.
func (r *NodeReports) Applies(cluster string, a Applied) {
	r.mu.Lock()
	defer r.mu.Unlock()
	views := r.views[cluster]
	if i := slices.IndexFunc(views, func(v Applied) bool { return v.Name == a.Name }); i >= 0 {
		views[i] = a
		return
	}
	r.views[cluster] = append(views, a)
}

// AppliesHead is Applies of app at repo's head, listing the objects of every file there.
func (r *NodeReports) AppliesHead(cluster, app string, repo *Repository, set ...Object) {
	r.Applies(cluster, Applied{Name: app, Repo: repo, Set: set})
}

func (r *NodeReports) FailWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *NodeReports) NodeReports(_ context.Context, d contract.Desired, cluster string) ([]observe.NodeReport, error) {
	answered, err := r.answer(d, cluster)
	if err != nil {
		return nil, err
	}
	out := make([]observe.NodeReport, 0, len(answered))
	for _, n := range answered {
		out = append(out, n.written())
	}
	return out, nil
}

// AddTo is reports, each carrying the argocd member of the newest report r answers for cluster unless it carries its own, as one node report holds a block's own members beside Argo CD's.
func (r *NodeReports) AddTo(_ context.Context, d contract.Desired, cluster string, reports []observe.NodeReport) ([]observe.NodeReport, error) {
	answered, err := r.answer(d, cluster)
	if err != nil {
		return nil, err
	}
	if len(answered) == 0 {
		return reports, nil
	}
	newest := slices.MaxFunc(answered, func(a, b nodeReport) int { return a.madeAt.Compare(b.madeAt) })
	out := slices.Clone(reports)
	for i := range out {
		if out[i].Body, err = withArgoCD(out[i], newest.argocd); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *NodeReports) answer(d contract.Desired, cluster string) ([]nodeReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	answered := slices.Clone(r.reports[cluster])
	if len(r.views[cluster]) == 0 {
		return answered, nil
	}
	apps := make([]Application, 0, len(r.views[cluster]))
	for _, view := range r.views[cluster] {
		app, err := view.Application()
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return append(answered, reportOf(d.VerbAt.Add(time.Minute), apps)), nil
}

// Application is a as a node report shows it now, for a report Report adds.
func (a Applied) Application() (Application, error) {
	revision, tree, err := a.Repo.treeAt(a.Revision)
	if err != nil {
		return Application{}, err
	}
	paths := a.Paths
	if paths == nil {
		paths = slices.Sorted(maps.Keys(tree))
	}
	var declared []gitops.Object
	for _, path := range paths {
		content, held := tree[path]
		if !held {
			continue
		}
		objects, err := gitops.Objects(content)
		if err != nil {
			return Application{}, fmt.Errorf("gitopstest: reading the objects %s declares at %s: %w", path, revision, err)
		}
		declared = append(declared, objects...)
	}
	app := Application{Name: a.Name, Revision: revision, Objects: []Object{}}
	for _, o := range declared {
		app.Objects = append(app.Objects, stateOf(o, a.Set))
	}
	for _, s := range a.Set {
		if !slices.Contains(declared, s.Object) {
			app.Objects = append(app.Objects, s)
		}
	}
	return app, nil
}

func stateOf(o gitops.Object, set []Object) Object {
	for _, s := range set {
		if s.Object == o {
			return s
		}
	}
	return Healthy(o)
}

func withArgoCD(report observe.NodeReport, argocd json.RawMessage) (json.RawMessage, error) {
	var body map[string]json.RawMessage
	if json.Unmarshal(report.Body, &body) != nil || body == nil {
		return nil, fmt.Errorf("gitopstest: the body of the node report received at %s is no JSON object", report.ReceivedAt.Format(time.RFC3339))
	}
	if _, own := body["argocd"]; own {
		return report.Body, nil
	}
	body["argocd"] = argocd
	return json.Marshal(body)
}

func reportOf(madeAt time.Time, apps []Application) nodeReport {
	listed := make([]map[string]any, 0, len(apps))
	for _, a := range apps {
		listed = append(listed, a.body())
	}
	argocd, _ := json.Marshal(map[string]any{"applications": listed})
	return nodeReport{madeAt: madeAt, argocd: argocd}
}

func (n nodeReport) written() observe.NodeReport {
	body, _ := json.Marshal(map[string]any{"madeAt": n.madeAt.Format(time.RFC3339Nano), "argocd": n.argocd})
	return observe.NodeReport{ReceivedAt: n.madeAt.Add(time.Second), Body: body}
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
			resource["health"] = nullable(o.Health)
		}
		if !o.HealthLeftOut && o.Health != "" && o.Health != "Healthy" {
			resource["message"] = nullable(o.Message)
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
