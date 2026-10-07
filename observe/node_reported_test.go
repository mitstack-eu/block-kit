package observe_test

// node_reported_test.go pins observe.NodeReported, the observer for a
// resource the platform has no route to, read from the node reports its own
// side sends (GET /v1/subscriptions/{id}/node-reports serves
// {"reports":[{"receivedAt","body"}]}, each body listing
// subscriptionResources). Only the newest report counts, and only when it
// was received after the verb: then the identity's object is present when
// the report lists it for the subscription and absent when it does not.
// A newest report no newer than the verb, or none, is unknown.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/observe"
)

var _ handler.Observer = observe.NodeReported{}

type fakeReports struct {
	reports []observe.NodeReport
	asked   []string
	err     error
}

func (f *fakeReports) NodeReports(_ context.Context, _ contract.Desired, subscriptionID string) ([]observe.NodeReport, error) {
	f.asked = append(f.asked, subscriptionID)
	return f.reports, f.err
}

var verbAt = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// report is a node report received at verbAt plus offset listing items.
func report(t *testing.T, offset time.Duration, items ...map[string]string) observe.NodeReport {
	t.Helper()
	list := make([]map[string]string, 0, len(items))
	list = append(list, items...)
	body, err := json.Marshal(map[string]any{"subscriptionResources": list})
	if err != nil {
		t.Fatal(err)
	}
	return observe.NodeReport{ReceivedAt: verbAt.Add(offset), Body: body}
}

var (
	appNamespace = map[string]string{"namespace": "", "kind": "Namespace", "name": "app-alpha", "subscription": "sub-1"}
	otherSubs    = map[string]string{"namespace": "", "kind": "Namespace", "name": "app-alpha", "subscription": "sub-2"}
	otherObject  = map[string]string{"namespace": "app-alpha", "kind": "ServiceAccount", "name": "app-alpha-runtime", "subscription": "sub-1"}
)

func namespaceRef() contract.ResourceRef {
	r := refFor("kubernetes.namespace", map[string]string{"kind": "Namespace", "name": "app-alpha"})
	r.Desired.VerbAt = verbAt
	return r
}

func TestNodeReported_CountsOnlyTheNewestReportReceivedAfterTheVerb(t *testing.T) {
	cases := []struct {
		label   string
		reports func(t *testing.T) []observe.NodeReport
		want    contract.State
	}{
		{"a newer report listing it", func(t *testing.T) []observe.NodeReport {
			return []observe.NodeReport{report(t, time.Minute, otherObject, appNamespace)}
		}, contract.Present},
		{"a newer report not listing it", func(t *testing.T) []observe.NodeReport {
			return []observe.NodeReport{report(t, time.Minute, otherObject)}
		}, contract.Absent},
		{"a newer report listing it for another subscription", func(t *testing.T) []observe.NodeReport {
			return []observe.NodeReport{report(t, time.Minute, otherSubs)}
		}, contract.Absent},
		{"an older report listing it", func(t *testing.T) []observe.NodeReport {
			return []observe.NodeReport{report(t, -time.Minute, appNamespace)}
		}, contract.Unknown},
		{"an older report not listing it", func(t *testing.T) []observe.NodeReport {
			return []observe.NodeReport{report(t, -time.Minute)}
		}, contract.Unknown},
		{"a report received at the verb", func(t *testing.T) []observe.NodeReport {
			return []observe.NodeReport{report(t, 0)}
		}, contract.Unknown},
		{"no report", func(t *testing.T) []observe.NodeReport { return nil }, contract.Unknown},
		{"the newest of several, listed last", func(t *testing.T) []observe.NodeReport {
			return []observe.NodeReport{report(t, time.Minute, appNamespace), report(t, 2*time.Minute)}
		}, contract.Absent},
		{"the newest of several, listed first", func(t *testing.T) []observe.NodeReport {
			return []observe.NodeReport{report(t, 2*time.Minute, appNamespace), report(t, time.Minute)}
		}, contract.Present},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			api := &fakeReports{reports: tc.reports(t)}
			got, err := observe.NodeReported{API: api}.ObserveResource(context.Background(), namespaceRef())
			wantObservation(t, got, err, tc.want, nil, "")
			if len(api.asked) != 1 || api.asked[0] != "sub-1" {
				t.Errorf("asked for the reports of %q, want sub-1 once", api.asked)
			}
		})
	}
}

func TestNodeReported_AnIdentityNamingNoObjectIsUnknownAndAsksNothing(t *testing.T) {
	api := &fakeReports{}
	r := namespaceRef()
	r.Identity = map[string]string{"kind": "Namespace"}
	got, err := observe.NodeReported{API: api}.ObserveResource(context.Background(), r)
	wantObservation(t, got, err, contract.Unknown, nil, "")
	if len(api.asked) != 0 {
		t.Errorf("asked for reports %q, want no call without a name", api.asked)
	}
}

func TestNodeReported_ReturnsTheAPIsError(t *testing.T) {
	boom := errors.New("platform: 503")
	_, err := observe.NodeReported{API: &fakeReports{err: boom}}.ObserveResource(context.Background(), namespaceRef())
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the API's error", err)
	}
}
