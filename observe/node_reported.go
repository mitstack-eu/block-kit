package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/bounded"
	"github.com/mitstack-eu/block-kit/helpers/events"
)

// NodeReport is one report as GET /v1/subscriptions/{id}/node-reports serves it, with the verdict the platform stored.
type NodeReport struct {
	ReceivedAt time.Time       `json:"receivedAt"`
	Body       json.RawMessage `json:"body"`
	Verdict    json.RawMessage `json:"verdict,omitempty"`
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
	raw, err := bounded.Read(resp, "the node reports of "+subscriptionID, maxNodeReportsBody)
	if err != nil {
		return ClusterReports{}, fmt.Errorf("observe: %w", err)
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
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

// Health is Argo CD's health of one object an Application applied, as the newest node report carries it, with Argo CD's Message beside any Status but Healthy.
type Health struct {
	// Status is Argo CD's status, as Healthy, Progressing, Degraded or Missing, empty for a kind Argo CD runs no health check for, a Secret among them, and HealthUnknown when the report carries none.
	Status  string
	Message string
}

// HealthUnknown is the Status of an object whose health the report does not carry, as one from an earlier talos-cluster-kit release.
const HealthUnknown = "Unknown"

var unreadHealth = Health{Status: HealthUnknown}

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
	// Health stays raw, since a report that carries no health leaves it out and one whose Argo CD runs no health check for the object's kind holds null.
	Health  json.RawMessage `json:"health"`
	Message string          `json:"message"`
}

func (o appliedObject) health() Health {
	var status *string
	if len(o.Health) == 0 || json.Unmarshal(o.Health, &status) != nil {
		return unreadHealth
	}
	if status == nil {
		return Health{}
	}
	return Health{Status: *status, Message: o.Message}
}

// is reports whether o is the object kubectlKind names as kubectl does, its kind in any case and its API group after a dot.
func (o appliedObject) is(kubectlKind, namespace, name string) bool {
	kind, group, _ := strings.Cut(kubectlKind, ".")
	return strings.EqualFold(o.Kind, kind) && o.Group == group && o.Namespace == namespace && o.Name == name
}

func (o NodeReported) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	obs, _, err := o.ObserveHealth(ctx, r)
	return obs, err
}

// ObserveHealth observes r as ObserveResource does and also returns Argo CD's health of an object the identity's Argo CD Application lists, and HealthUnknown beside any other observation.
func (o NodeReported) ObserveHealth(ctx context.Context, r contract.ResourceRef) (contract.Observation, Health, error) {
	ids, obs, ok := identityKeys(r, "kind", "name")
	if !ok {
		return obs, unreadHealth, nil
	}
	newest, obs, err := o.newest(ctx, r)
	if err != nil || newest == nil {
		return obs, unreadHealth, err
	}
	if app := r.Identity["argocdApplication"]; app != "" {
		obs, health := appliedBy(r, *newest, app, ids[0], ids[1])
		return obs, health, nil
	}
	return labelled(r, *newest, ids[0], ids[1]), unreadHealth, nil
}

// newest is the newest report of the identity's cluster made since the verb, or nil with the unknown observation that says why there is none.
func (o NodeReported) newest(ctx context.Context, r contract.ResourceRef) (*reportBody, contract.Observation, error) {
	cluster := r.Identity["clusterSubscription"]
	if cluster == "" {
		cluster = r.Desired.SubscriptionID
	}
	got, err := o.reports(ctx, r.Desired, cluster)
	if err != nil {
		return nil, contract.Observation{}, err
	}
	var newest *reportBody
	var newestAt time.Time
	for _, report := range got.Reports {
		var body reportBody
		if err := json.Unmarshal(report.Body, &body); err != nil {
			return nil, contract.Observation{}, fmt.Errorf("observe: reading the node report received at %s: %w", report.ReceivedAt.Format(time.RFC3339), err)
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
		return nil, unknown("resource %q: no node report made since the verb has arrived%s", r.Name, enrolled(cluster, got.Enrolment)), nil
	}
	return newest, contract.Observation{}, nil
}

// labelled reads the object of kind and name from the objects labelled with the observing subscription's id in the newest counting report.
func labelled(r contract.ResourceRef, newest reportBody, kind, name string) contract.Observation {
	var listed []reportedObject
	if json.Unmarshal(newest.SubscriptionResources, &listed) != nil {
		return unknown("resource %q: the newest node report could not list the subscription's objects", r.Name)
	}
	want := reportedObject{Namespace: r.Identity["namespace"], Kind: kind, Name: name, Subscription: r.Desired.SubscriptionID}
	for _, item := range listed {
		if item == want {
			return present(r, nil)
		}
	}
	if newest.MadeAt == nil {
		return unknown("resource %q: the newest node report does not list it, but proves no absence: it carries no madeAt", r.Name)
	}
	if !newest.SubscriptionResourcesComplete && !slices.Contains(newest.SubscriptionResourceKinds, want.Kind) {
		return unknown("resource %q: the newest node report does not list it, but proves no absence: it neither marks its list complete nor read kind %q whole", r.Name, want.Kind)
	}
	return absent()
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

// appliedBy reads the object of kind and name, with Argo CD's health of it, from the list of the Application app in the newest counting report.
func appliedBy(r contract.ResourceRef, newest reportBody, app, kind, name string) (contract.Observation, Health) {
	var argo struct {
		Applications []reportedApplication `json:"applications"`
	}
	if json.Unmarshal(newest.ArgoCD, &argo) != nil {
		return unknown("resource %q: the newest node report could not list Argo CD's Applications", r.Name), unreadHealth
	}
	i := slices.IndexFunc(argo.Applications, func(a reportedApplication) bool { return a.Name == app })
	if i < 0 || argo.Applications[i].Resources == nil {
		return unknown("resource %q: the newest node report lists no objects the Argo CD Application %q applied", r.Name, app), unreadHealth
	}
	application := argo.Applications[i]
	for _, o := range *application.Resources {
		if o.is(kind, r.Identity["namespace"], name) {
			return present(r, nil), o.health()
		}
	}
	if newest.MadeAt == nil {
		return unknown("resource %q: the Argo CD Application %q does not list it, but the newest node report proves no absence: it carries no madeAt", r.Name, app), unreadHealth
	}
	if application.Sync != "Synced" {
		return unknown("resource %q: the Argo CD Application %q does not list it, but proves no absence until it reads Synced, not %q", r.Name, app, application.Sync), unreadHealth
	}
	return absent(), unreadHealth
}
