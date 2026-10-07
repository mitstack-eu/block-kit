package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
)

// NodeReport is one report a node sent the platform about the objects it
// holds, as GET /v1/subscriptions/{id}/node-reports serves it.
type NodeReport struct {
	ReceivedAt time.Time
	Body       json.RawMessage
}

// NodeReportsAPI returns the node reports the platform holds for a
// subscription.
type NodeReportsAPI interface {
	NodeReports(ctx context.Context, d contract.Desired, subscriptionID string) ([]NodeReport, error)
}

// NodeReported observes a resource the platform has no route to, such as a
// kubernetes.namespace, by the kind, name and optional namespace its
// identity names, from the reports the node it lives on sends. Only the
// newest report counts, and only when it was received after the verb began:
// the object is present when that report lists it for the subscription and
// absent when it does not. Otherwise it is unknown.
type NodeReported struct {
	API NodeReportsAPI
}

// reportedObject is one object a node report lists.
type reportedObject struct {
	Namespace    string `json:"namespace"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Subscription string `json:"subscription"`
}

// ObserveResource implements handler.Observer.
func (o NodeReported) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	ids, obs, ok := identityKeys(r, "kind", "name")
	if !ok {
		return obs, nil
	}
	reports, err := o.API.NodeReports(ctx, r.Desired, r.Desired.SubscriptionID)
	if err != nil {
		return contract.Observation{}, err
	}
	var newest *NodeReport
	for i := range reports {
		if newest == nil || reports[i].ReceivedAt.After(newest.ReceivedAt) {
			newest = &reports[i]
		}
	}
	if newest == nil || !newest.ReceivedAt.After(r.Desired.VerbAt) {
		return unknown("resource %q: no node report has arrived since the verb began", r.Name), nil
	}
	var body struct {
		SubscriptionResources []reportedObject `json:"subscriptionResources"`
	}
	if err := json.Unmarshal(newest.Body, &body); err != nil {
		return contract.Observation{}, fmt.Errorf("observe: reading the node report received at %s: %w", newest.ReceivedAt.Format(time.RFC3339), err)
	}
	want := reportedObject{Namespace: r.Identity["namespace"], Kind: ids[0], Name: ids[1], Subscription: r.Desired.SubscriptionID}
	for _, item := range body.SubscriptionResources {
		if item == want {
			return present(nil), nil
		}
	}
	return absent(), nil
}
