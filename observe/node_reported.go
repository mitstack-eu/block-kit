package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/events"
)

// NodeReport is one report as GET /v1/subscriptions/{id}/node-reports serves it.
type NodeReport struct {
	ReceivedAt time.Time       `json:"receivedAt"`
	Body       json.RawMessage `json:"body"`
}

type NodeReportsAPI interface {
	NodeReports(ctx context.Context, d contract.Desired, subscriptionID string) ([]NodeReport, error)
}

// Enrolment is when a node spent its cluster's enrolment token, the zero time while none has.
type Enrolment struct {
	SpentAt time.Time
}

// ClusterReports is a cluster's node reports with its enrolment, nil when the platform's answer says nothing of it.
type ClusterReports struct {
	Reports   []NodeReport
	Enrolment *Enrolment
}

// ClusterReportsAPI is a NodeReportsAPI that also reads a cluster's enrolment, so NodeReported can say whether a cluster that sent no report enrolled a node.
type ClusterReportsAPI interface {
	ClusterReports(ctx context.Context, d contract.Desired, subscriptionID string) (ClusterReports, error)
}

// maxNodeReportsBody fits the platform's answer, which holds up to 50 reports of up to 256 KiB each.
const maxNodeReportsBody = 32 << 20

var nodeReportsHTTP = &http.Client{Timeout: 10 * time.Second}

// NodeReportsClient lists a subscription's node reports from the platform the request's events callback names, with the callback's token; a nil HTTPClient waits ten seconds at most.
type NodeReportsClient struct {
	HTTPClient *http.Client
}

func (c NodeReportsClient) NodeReports(ctx context.Context, d contract.Desired, subscriptionID string) ([]NodeReport, error) {
	listed, err := c.ClusterReports(ctx, d, subscriptionID)
	return listed.Reports, err
}

func (c NodeReportsClient) ClusterReports(ctx context.Context, _ contract.Desired, subscriptionID string) (ClusterReports, error) {
	base, token, err := events.PlatformFrom(ctx)
	if err != nil {
		return ClusterReports{}, fmt.Errorf("observe: reading the node reports of %s: %w", subscriptionID, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/subscriptions/"+url.PathEscape(subscriptionID)+"/node-reports", nil)
	if err != nil {
		return ClusterReports{}, fmt.Errorf("observe: building the node reports request for %s: %w", subscriptionID, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := c.HTTPClient
	if client == nil {
		client = nodeReportsHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return ClusterReports{}, fmt.Errorf("observe: reading the node reports of %s: %w", subscriptionID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ClusterReports{}, fmt.Errorf("observe: the platform answered %d for the node reports of %s", resp.StatusCode, subscriptionID)
	}
	var listed struct {
		Reports    []NodeReport    `json:"reports"`
		EnrolledAt json.RawMessage `json:"enrolledAt"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxNodeReportsBody)).Decode(&listed); err != nil {
		return ClusterReports{}, fmt.Errorf("observe: decoding the node reports of %s: %w", subscriptionID, err)
	}
	out := ClusterReports{Reports: listed.Reports}
	if listed.EnrolledAt != nil {
		var at *time.Time
		if err := json.Unmarshal(listed.EnrolledAt, &at); err != nil {
			return ClusterReports{}, fmt.Errorf("observe: decoding the enrolment of %s: %w", subscriptionID, err)
		}
		out.Enrolment = &Enrolment{}
		if at != nil {
			out.Enrolment.SpentAt = *at
		}
	}
	return out, nil
}

// NodeReported matches an object labelled with the observing subscription's id in the reports of the identity's clusterSubscription, else its own.
// A report counts when its madeAt, else its receivedAt, is after the verb, and the newest counting report decides.
// An unlisted object is absent only when that report has a madeAt after the verb and marks its list complete or names the object's kind as read whole.
// An identity holding argocdApplication is matched instead against the objects that Argo CD Application applied, and an object it does not list is absent only when that report has a madeAt after the verb and shows the Application Synced.
type NodeReported struct {
	API NodeReportsAPI
}

type reportedObject struct {
	Namespace    string `json:"namespace"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Subscription string `json:"subscription"`
}

type reportBody struct {
	MadeAt                        *time.Time      `json:"madeAt"`
	SubscriptionResources         json.RawMessage `json:"subscriptionResources"`
	SubscriptionResourcesComplete bool            `json:"subscriptionResourcesComplete"`
	SubscriptionResourceKinds     []string        `json:"subscriptionResourceKinds"`
	ArgoCD                        json.RawMessage `json:"argocd"`
}

// reportedApplication is one Argo CD Application; a nil Resources means the report carries no list of what it applied.
type reportedApplication struct {
	Name      string           `json:"name"`
	Sync      string           `json:"sync"`
	Resources *[]appliedObject `json:"resources"`
}

type appliedObject struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// is reports whether o is the object kubectlKind names as kubectl does, its kind in any case and its API group after a dot.
func (o appliedObject) is(kubectlKind, namespace, name string) bool {
	kind, group, _ := strings.Cut(kubectlKind, ".")
	return strings.EqualFold(o.Kind, kind) && o.Group == group && o.Namespace == namespace && o.Name == name
}

func (o NodeReported) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	ids, obs, ok := identityKeys(r, "kind", "name")
	if !ok {
		return obs, nil
	}
	cluster := r.Identity["clusterSubscription"]
	if cluster == "" {
		cluster = r.Desired.SubscriptionID
	}
	got, err := o.reports(ctx, r.Desired, cluster)
	if err != nil {
		return contract.Observation{}, err
	}
	var newest *reportBody
	var newestAt time.Time
	for _, report := range got.Reports {
		var body reportBody
		if err := json.Unmarshal(report.Body, &body); err != nil {
			return contract.Observation{}, fmt.Errorf("observe: reading the node report received at %s: %w", report.ReceivedAt.Format(time.RFC3339), err)
		}
		at := report.ReceivedAt
		if body.MadeAt != nil {
			at = *body.MadeAt
		}
		if at.After(r.Desired.VerbAt) && (newest == nil || at.After(newestAt)) {
			newest, newestAt = &body, at
		}
	}
	if newest == nil {
		return unknown("resource %q: no node report made since the verb has arrived%s", r.Name, enrolled(cluster, got.Enrolment)), nil
	}
	if app := r.Identity["argocdApplication"]; app != "" {
		return appliedBy(r, *newest, app, ids[0], ids[1]), nil
	}
	var listed []reportedObject
	if json.Unmarshal(newest.SubscriptionResources, &listed) != nil {
		return unknown("resource %q: the newest node report could not list the subscription's objects", r.Name), nil
	}
	want := reportedObject{Namespace: r.Identity["namespace"], Kind: ids[0], Name: ids[1], Subscription: r.Desired.SubscriptionID}
	for _, item := range listed {
		if item == want {
			return present(r, nil), nil
		}
	}
	if newest.MadeAt == nil {
		return unknown("resource %q: the newest node report does not list it, but proves no absence: it carries no madeAt", r.Name), nil
	}
	if !newest.SubscriptionResourcesComplete && !slices.Contains(newest.SubscriptionResourceKinds, want.Kind) {
		return unknown("resource %q: the newest node report does not list it, but proves no absence: it neither marks its list complete nor read kind %q whole", r.Name, want.Kind), nil
	}
	return absent(), nil
}

func (o NodeReported) reports(ctx context.Context, d contract.Desired, cluster string) (ClusterReports, error) {
	if api, ok := o.API.(ClusterReportsAPI); ok {
		return api.ClusterReports(ctx, d, cluster)
	}
	reports, err := o.API.NodeReports(ctx, d, cluster)
	return ClusterReports{Reports: reports}, err
}

func enrolled(cluster string, e *Enrolment) string {
	switch {
	case e == nil:
		return ""
	case e.SpentAt.IsZero():
		return fmt.Sprintf(", and cluster subscription %q has enrolled no node with the platform", cluster)
	}
	return fmt.Sprintf(", though cluster subscription %q enrolled a node at %s", cluster, e.SpentAt.UTC().Format(time.RFC3339))
}

// appliedBy reads the object of kind and name from the list of the Application app in the newest counting report.
func appliedBy(r contract.ResourceRef, newest reportBody, app, kind, name string) contract.Observation {
	var argo struct {
		Applications []reportedApplication `json:"applications"`
	}
	if json.Unmarshal(newest.ArgoCD, &argo) != nil {
		return unknown("resource %q: the newest node report could not list Argo CD's Applications", r.Name)
	}
	i := slices.IndexFunc(argo.Applications, func(a reportedApplication) bool { return a.Name == app })
	if i < 0 || argo.Applications[i].Resources == nil {
		return unknown("resource %q: the newest node report lists no objects the Argo CD Application %q applied", r.Name, app)
	}
	application := argo.Applications[i]
	for _, o := range *application.Resources {
		if o.is(kind, r.Identity["namespace"], name) {
			return present(r, nil)
		}
	}
	if newest.MadeAt == nil {
		return unknown("resource %q: the Argo CD Application %q does not list it, but the newest node report proves no absence: it carries no madeAt", r.Name, app)
	}
	if application.Sync != "Synced" {
		return unknown("resource %q: the Argo CD Application %q does not list it, but proves no absence until it reads Synced, not %q", r.Name, app, application.Sync)
	}
	return absent()
}
