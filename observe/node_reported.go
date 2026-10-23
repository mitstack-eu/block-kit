package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/events"
)

// NodeReport is one report as GET /v1/subscriptions/{id}/node-reports serves it.
type NodeReport struct {
	ReceivedAt time.Time       `json:"receivedAt"`
	Body       json.RawMessage `json:"body"`
}

type NodeReportsAPI interface {
	NodeReports(ctx context.Context, d contract.Desired, subscriptionID string) ([]NodeReport, error)
}

// maxNodeReportsBody fits the platform's answer, which holds up to 50 reports of up to 256 KiB each.
const maxNodeReportsBody = 32 << 20

var nodeReportsHTTP = &http.Client{Timeout: 10 * time.Second}

// NodeReportsClient lists a subscription's node reports from the platform the request's events callback names, with the callback's token; a nil HTTPClient waits ten seconds at most.
type NodeReportsClient struct {
	HTTPClient *http.Client
}

func (c NodeReportsClient) NodeReports(ctx context.Context, _ contract.Desired, subscriptionID string) ([]NodeReport, error) {
	base, token, err := events.PlatformFrom(ctx)
	if err != nil {
		return nil, fmt.Errorf("observe: reading the node reports of %s: %w", subscriptionID, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/subscriptions/"+url.PathEscape(subscriptionID)+"/node-reports", nil)
	if err != nil {
		return nil, fmt.Errorf("observe: building the node reports request for %s: %w", subscriptionID, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := c.HTTPClient
	if client == nil {
		client = nodeReportsHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("observe: reading the node reports of %s: %w", subscriptionID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("observe: the platform answered %d for the node reports of %s", resp.StatusCode, subscriptionID)
	}
	var listed struct {
		Reports []NodeReport `json:"reports"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxNodeReportsBody)).Decode(&listed); err != nil {
		return nil, fmt.Errorf("observe: decoding the node reports of %s: %w", subscriptionID, err)
	}
	return listed.Reports, nil
}

// NodeReported matches an object labelled with the observing subscription's id in the reports of the identity's clusterSubscription, else its own.
// A report counts when its madeAt, else its receivedAt, is after the verb, and the newest counting report decides.
// An unlisted object is absent only when that report has a madeAt after the verb and marks its list complete.
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
	reports, err := o.API.NodeReports(ctx, r.Desired, cluster)
	if err != nil {
		return contract.Observation{}, err
	}
	var newest *reportBody
	var newestAt time.Time
	for _, report := range reports {
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
		return unknown("resource %q: no node report made since the verb has arrived", r.Name), nil
	}
	var listed []reportedObject
	if json.Unmarshal(newest.SubscriptionResources, &listed) != nil {
		return unknown("resource %q: the newest node report could not list the subscription's objects", r.Name), nil
	}
	want := reportedObject{Namespace: r.Identity["namespace"], Kind: ids[0], Name: ids[1], Subscription: r.Desired.SubscriptionID}
	for _, item := range listed {
		if item == want {
			return present(nil), nil
		}
	}
	if newest.MadeAt == nil || !newest.SubscriptionResourcesComplete {
		return unknown("resource %q: the newest node report does not list it, but proves no absence: it carries no madeAt or does not mark its list complete", r.Name), nil
	}
	return absent(), nil
}
