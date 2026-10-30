package observe_test

// A report counts only when its madeAt, else its receivedAt, is after the verb, an object is absent only when a report made after the verb marks its list complete or names the object's kind as read whole, or shows the Argo CD Application its identity names Synced without it, a cluster that sent none is named enrolled or not as the platform records it, and the client reads the reports and their verdicts with the events callback's token.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/events"
	"github.com/mitstack-eu/block-kit/observe"
)

var _ handler.Observer = observe.NodeReported{}

type fakeReports struct {
	reports []observe.NodeReport
	bySub   map[string][]observe.NodeReport
	asked   []string
	err     error
}

func (f *fakeReports) NodeReports(_ context.Context, _ contract.Desired, subscriptionID string) ([]observe.NodeReport, error) {
	f.asked = append(f.asked, subscriptionID)
	if f.bySub != nil {
		return f.bySub[subscriptionID], f.err
	}
	return f.reports, f.err
}

var verbAt = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// nodeReport describes one report: when the platform received it, when the node made it, whether it marks its list complete, which kinds it read whole, and the list itself; an unset field leaves its key out of the body.
type nodeReport struct {
	received time.Duration
	made     *time.Duration
	complete *bool
	kinds    []string
	items    any
}

func at(d time.Duration) *time.Duration { return &d }
func flag(b bool) *bool                 { return &b }

func (r nodeReport) build(t *testing.T) observe.NodeReport {
	t.Helper()
	body := map[string]any{"subscriptionResources": r.items}
	if r.made != nil {
		body["madeAt"] = verbAt.Add(*r.made).Format(time.RFC3339Nano)
	}
	if r.complete != nil {
		body["subscriptionResourcesComplete"] = *r.complete
	}
	if r.kinds != nil {
		body["subscriptionResourceKinds"] = r.kinds
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return observe.NodeReport{ReceivedAt: verbAt.Add(r.received), Body: raw}
}

var (
	appNamespace = map[string]string{"namespace": "", "kind": "Namespace", "name": "app-alpha", "subscription": "sub-1"}
	otherSubs    = map[string]string{"namespace": "", "kind": "Namespace", "name": "app-alpha", "subscription": "sub-2"}
	otherObject  = map[string]string{"namespace": "app-alpha", "kind": "ServiceAccount", "name": "app-alpha-runtime", "subscription": "sub-1"}
	readFailed   = map[string]any{"exitStatus": 1, "stderr": "error: You must be logged in to the server (Unauthorized)"}
)

func list(items ...map[string]string) []map[string]string {
	return append([]map[string]string{}, items...)
}

func namespaceRef() contract.ResourceRef {
	r := refFor("kubernetes.namespace", map[string]string{"kind": "Namespace", "name": "app-alpha"})
	r.Desired.VerbAt = verbAt
	return r
}

func TestNodeReported_ClaimsAbsenceOnlyFromAReportMadeAfterTheVerbThatReadTheObjectsKindWhole(t *testing.T) {
	cases := []struct {
		label   string
		reports []nodeReport
		want    contract.State
		detail  string
	}{
		{"a report received after the verb listing it",
			[]nodeReport{{received: time.Minute, items: list(otherObject, appNamespace)}}, contract.Present, ""},
		{"a complete report made after the verb not listing it",
			[]nodeReport{{received: time.Minute, made: at(30 * time.Second), complete: flag(true), items: list(otherObject)}}, contract.Absent, ""},
		{"a complete report made after the verb listing it for another subscription",
			[]nodeReport{{received: time.Minute, made: at(30 * time.Second), complete: flag(true), items: list(otherSubs)}}, contract.Absent, ""},
		{"a report with no made-at time not listing it",
			[]nodeReport{{received: time.Minute, complete: flag(true), items: list(otherObject)}}, contract.Unknown, "absence"},
		{"a report made after the verb that may be truncated",
			[]nodeReport{{received: time.Minute, made: at(30 * time.Second), items: list(otherObject)}}, contract.Unknown, "absence"},
		{"a report made after the verb marked incomplete",
			[]nodeReport{{received: time.Minute, made: at(30 * time.Second), complete: flag(false), items: list(otherObject)}}, contract.Unknown, "absence"},
		{"a report made after the verb that read its kind whole", []nodeReport{{received: time.Minute, made: at(30 * time.Second), kinds: []string{"ServiceAccount", "Namespace"}, items: list(otherObject)}}, contract.Absent, ""},
		{"a report made after the verb that read only other kinds whole", []nodeReport{{received: time.Minute, made: at(30 * time.Second), kinds: []string{"ServiceAccount", "namespace"}, items: list(otherObject)}}, contract.Unknown, `kind "Namespace"`},
		{"a report with no made-at time that read its kind whole", []nodeReport{{received: time.Minute, kinds: []string{"Namespace"}, items: list(otherObject)}}, contract.Unknown, "madeAt"},
		{"a report the node could not read the cluster for",
			[]nodeReport{{received: time.Minute, made: at(30 * time.Second), complete: flag(true), items: readFailed}}, contract.Unknown, ""},
		{"a report made before the verb, received after it, listing it",
			[]nodeReport{{received: time.Minute, made: at(-30 * time.Second), complete: flag(true), items: list(appNamespace)}}, contract.Unknown, ""},
		{"a report received before the verb listing it",
			[]nodeReport{{received: -time.Minute, items: list(appNamespace)}}, contract.Unknown, ""},
		{"a report received at the verb",
			[]nodeReport{{received: 0, items: list()}}, contract.Unknown, ""},
		{"no report", nil, contract.Unknown, ""},
		{"the newest of several proves it gone",
			[]nodeReport{
				{received: 2 * time.Minute, made: at(time.Minute), complete: flag(true), items: list(appNamespace)},
				{received: 2 * time.Minute, made: at(90 * time.Second), complete: flag(true), items: list()},
			}, contract.Absent, ""},
		{"the newest of several lists it",
			[]nodeReport{
				{received: 3 * time.Minute, made: at(2 * time.Minute), complete: flag(true), items: list(appNamespace)},
				{received: 3 * time.Minute, made: at(time.Minute), complete: flag(true), items: list()},
			}, contract.Present, ""},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			reports := make([]observe.NodeReport, 0, len(tc.reports))
			for _, r := range tc.reports {
				reports = append(reports, r.build(t))
			}
			api := &fakeReports{reports: reports}
			ref := namespaceRef()
			got, err := observe.NodeReported{API: api}.ObserveResource(context.Background(), ref)
			wantObservation(t, ref, got, err, tc.want, nil, tc.detail)
			if len(api.asked) != 1 || api.asked[0] != "sub-1" {
				t.Errorf("asked for the reports of %q, want sub-1 once", api.asked)
			}
		})
	}
}

// Node reports are stored under the cluster's own subscription, whose nodes send them, while a namespace carries the label of the subscription that created it, here sub-1.
func TestNodeReported_ReadsTheReportsOfTheClusterSubscriptionTheIdentityNames(t *testing.T) {
	complete := func(items ...map[string]any) nodeReport {
		return nodeReport{received: time.Minute, made: at(30 * time.Second), complete: flag(true), items: append([]map[string]any{}, items...)}
	}
	nullNamespace := map[string]any{"namespace": nil, "kind": "Namespace", "name": "app-alpha", "subscription": "sub-1"}
	noNamespace := map[string]any{"kind": "Namespace", "name": "app-alpha", "subscription": "sub-1"}
	clusterOwn := map[string]any{"kind": "Namespace", "name": "app-alpha", "subscription": "sub-cluster"}
	cases := []struct {
		label     string
		cluster   string
		bySub     map[string]nodeReport
		wantAsked string
		want      contract.State
	}{
		{"another block's namespace with a null namespace", "sub-cluster",
			map[string]nodeReport{"sub-cluster": complete(nullNamespace), "sub-1": complete()}, "sub-cluster", contract.Present},
		{"another block's namespace with no namespace key", "sub-cluster",
			map[string]nodeReport{"sub-cluster": complete(noNamespace), "sub-1": complete()}, "sub-cluster", contract.Present},
		{"a namespace labelled with the cluster's own subscription", "sub-cluster",
			map[string]nodeReport{"sub-cluster": complete(clusterOwn), "sub-1": complete(noNamespace)}, "sub-cluster", contract.Absent},
		{"no cluster subscription named, so the observing subscription's own", "",
			map[string]nodeReport{"sub-cluster": complete(), "sub-1": complete(noNamespace)}, "sub-1", contract.Present},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			api := &fakeReports{bySub: map[string][]observe.NodeReport{}}
			for sub, r := range tc.bySub {
				api.bySub[sub] = []observe.NodeReport{r.build(t)}
			}
			ref := namespaceRef()
			if tc.cluster != "" {
				ref.Identity["clusterSubscription"] = tc.cluster
			}
			got, err := observe.NodeReported{API: api}.ObserveResource(context.Background(), ref)
			wantObservation(t, ref, got, err, tc.want, nil, "")
			if len(api.asked) != 1 || api.asked[0] != tc.wantAsked {
				t.Errorf("asked for the reports of %q, want %s once", api.asked, tc.wantAsked)
			}
		})
	}
}

func TestNodeReported_AnIdentityNamingNoObjectIsUnknownAndAsksNothing(t *testing.T) {
	api := &fakeReports{}
	r := namespaceRef()
	r.Identity = map[string]string{"kind": "Namespace"}
	got, err := observe.NodeReported{API: api}.ObserveResource(context.Background(), r)
	wantObservation(t, r, got, err, contract.Unknown, nil, "")
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

var _ observe.NodeReportsAPI = observe.NodeReportsClient{}

const callbackToken = "callback-token-7f3a"

// platformServing answers a cluster subscription's node reports only to the callback's token, as the platform does.
func platformServing(t *testing.T, cluster string, reports ...observe.NodeReport) *httptest.Server {
	t.Helper()
	listed := make([]map[string]any, 0, len(reports))
	for _, r := range reports {
		item := map[string]any{"receivedAt": r.ReceivedAt.Format(time.RFC3339Nano), "body": r.Body}
		if r.Verdict != nil {
			item["verdict"] = r.Verdict
		}
		listed = append(listed, item)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("Authorization") != "Bearer "+callbackToken:
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method != http.MethodGet || r.URL.Path != "/v1/subscriptions/"+cluster+"/node-reports":
			http.NotFound(w, r)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"reports": listed})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withCallback(url, token string) context.Context {
	return events.WithEnvelope(context.Background(), events.Envelope{SubscriptionID: "sub-1", Callback: &events.Callback{URL: url, Token: token}})
}

func TestNodeReportsClient_ReadsTheReportsAndTheirVerdictsWithTheCallbacksToken(t *testing.T) {
	want := nodeReport{received: time.Minute, made: at(30 * time.Second), complete: flag(true), items: list(appNamespace)}.build(t)
	want.Verdict = json.RawMessage(`{"reportSignature":{"status":"signed"}}`)
	platform := platformServing(t, "sub-cluster", want)
	client := observe.NodeReportsClient{HTTPClient: platform.Client()}

	got, err := client.NodeReports(withCallback(platform.URL+"/v1/webhooks/operator-events", callbackToken), contract.Desired{}, "sub-cluster")

	if err != nil {
		t.Fatalf("NodeReports error = %v, want the reports the platform serves", err)
	}
	if len(got) != 1 || !got[0].ReceivedAt.Equal(want.ReceivedAt) || string(got[0].Body) != string(want.Body) || string(got[0].Verdict) != string(want.Verdict) {
		t.Errorf("NodeReports = %+v, want [%+v]", got, want)
	}
}

func TestNodeReportsClient_IsAnErrorCarryingNoTokenWhenThePlatformCannotAnswer(t *testing.T) {
	platform := platformServing(t, "sub-cluster")
	client := observe.NodeReportsClient{HTTPClient: platform.Client()}
	callback := platform.URL + "/v1/webhooks/operator-events"
	cases := []struct {
		name, subscription string
		ctx                context.Context
	}{
		{"a request carrying no events callback", "sub-cluster", context.Background()},
		{"a subscription the platform serves no reports for", "sub-other", withCallback(callback, callbackToken)},
		{"a token the platform refuses", "sub-cluster", withCallback(callback, "stale-token-0c1d")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.NodeReports(tc.ctx, contract.Desired{}, tc.subscription)

			if err == nil || strings.Contains(err.Error(), callbackToken) || strings.Contains(err.Error(), "stale-token-0c1d") {
				t.Errorf("NodeReports = %+v, %v; want an error carrying no token", got, err)
			}
		})
	}
}

func TestNodeReported_NamesWhetherAClusterThatSentNoReportEnrolledANode(t *testing.T) {
	silent := `resource "kubernetes.namespace": no node report made since the verb has arrived`
	cases := []struct{ label, listing, want string }{
		{"a node enrolled", `{"reports": [], "enrolledAt": "2026-10-07T11:00:00Z"}`, silent + `, though cluster subscription "sub-1" enrolled a node at 2026-10-07T11:00:00Z`},
		{"no node enrolled", `{"reports": [], "enrolledAt": null}`, silent + `, and cluster subscription "sub-1" has enrolled no node with the platform`},
		{"a platform that says nothing of enrolment", `{"reports": []}`, silent},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			platform := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.listing) }))
			defer platform.Close()
			observer := observe.NodeReported{API: observe.NodeReportsClient{HTTPClient: platform.Client()}}

			got, err := observer.ObserveResource(withCallback(platform.URL+"/v1/webhooks/operator-events", callbackToken), namespaceRef())

			if err != nil || got.State != contract.Unknown || got.StatusDetail != tc.want {
				t.Errorf("ObserveResource = %+v, %v; want unknown, detail %q", got, err, tc.want)
			}
		})
	}
}

// argoReport is a node report body carrying Argo CD's Applications as the talos-cluster-kit report passes them on; a nil made leaves out madeAt.
func argoReport(made *time.Duration, apps ...map[string]any) map[string]any {
	body := map[string]any{"argocd": map[string]any{"applications": apps}}
	if made != nil {
		body["madeAt"] = verbAt.Add(*made).Format(time.RFC3339Nano)
	}
	return body
}

// application is an Application entry; a nil applied leaves out its resources key.
func application(name, sync string, applied ...map[string]any) map[string]any {
	app := map[string]any{"name": name, "health": "Healthy", "sync": sync}
	if applied != nil {
		app["resources"] = applied
	}
	return app
}

func appliedObject(group, kind, namespace string) map[string]any {
	return map[string]any{"group": group, "kind": kind, "namespace": namespace, "name": "app-repository-alpha", "sync": "Synced"}
}

func TestNodeReported_ReadsAnObjectAnArgoCDApplicationAppliedFromThatApplicationsList(t *testing.T) {
	secret, role, made, none := appliedObject("", "Secret", "argocd"), appliedObject("rbac.authorization.k8s.io", "Role", "argocd"), at(30*time.Second), []map[string]any{}
	creds := func(sync string, applied ...map[string]any) map[string]any {
		return application("repository-credentials", sync, applied...)
	}
	labelled := argoReport(made, creds("Synced", role))
	labelled["subscriptionResources"] = []map[string]string{{"namespace": "argocd", "kind": "secret", "name": "app-repository-alpha", "subscription": "sub-1"}}
	cases := []struct {
		label, kind, namespace string
		reports                []map[string]any
		want                   contract.State
		detail                 string
	}{
		{"the Application's list names it", "secret", "argocd", []map[string]any{argoReport(made, creds("OutOfSync", role, secret))}, contract.Present, ""},
		{"the list names it under its API group", "role.rbac.authorization.k8s.io", "argocd", []map[string]any{argoReport(made, creds("Synced", role))}, contract.Present, ""},
		{"a cluster-scoped object the list names with a null namespace", "namespace", "", []map[string]any{argoReport(made, creds("Synced", map[string]any{"group": "", "kind": "Namespace", "namespace": nil, "name": "app-repository-alpha"}))}, contract.Present, ""},
		{"a report made after the verb shows the Application Synced without it", "secret", "argocd", []map[string]any{argoReport(made, creds("Synced", role))}, contract.Absent, ""},
		{"only another Application's list names it", "secret", "argocd", []map[string]any{argoReport(made, creds("Synced", role), application("cluster-root", "Synced", secret))}, contract.Absent, ""},
		{"the list names it in another namespace", "secret", "argocd", []map[string]any{argoReport(made, creds("Synced", appliedObject("", "Secret", "app-alpha")))}, contract.Absent, ""},
		{"the list names it in another API group", "secret", "argocd", []map[string]any{argoReport(made, creds("Synced", appliedObject("example.com", "Secret", "argocd")))}, contract.Absent, ""},
		{"only the subscription's labelled objects list it", "secret", "argocd", []map[string]any{labelled}, contract.Absent, ""},
		{"the Application is OutOfSync without it", "secret", "argocd", []map[string]any{argoReport(made, creds("OutOfSync", role))}, contract.Unknown, "Synced"},
		{"a report with no made-at time shows the Application Synced without it", "secret", "argocd", []map[string]any{argoReport(nil, creds("Synced", role))}, contract.Unknown, "madeAt"},
		{"the report carries no list for the Application", "secret", "argocd", []map[string]any{argoReport(made, creds("Synced"))}, contract.Unknown, "repository-credentials"},
		{"the report does not name the Application", "secret", "argocd", []map[string]any{argoReport(made, application("cluster-root", "Synced", secret))}, contract.Unknown, "repository-credentials"},
		{"a report made before the verb names it", "secret", "argocd", []map[string]any{argoReport(at(-30*time.Second), creds("Synced", secret))}, contract.Unknown, ""},
		{"the newest of several shows the Application Synced without it", "secret", "argocd",
			[]map[string]any{argoReport(at(40*time.Second), creds("Synced", secret)), argoReport(at(50*time.Second), creds("Synced", none...))}, contract.Absent, ""},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			api := &fakeReports{}
			for _, body := range tc.reports {
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				api.reports = append(api.reports, observe.NodeReport{ReceivedAt: verbAt.Add(time.Minute), Body: raw})
			}
			ref := refFor("kubernetes.secret", map[string]string{"kind": tc.kind, "name": "app-repository-alpha", "namespace": tc.namespace, "clusterSubscription": "sub-cluster", "argocdApplication": "repository-credentials"})
			ref.Desired.VerbAt = verbAt

			got, err := observe.NodeReported{API: api}.ObserveResource(context.Background(), ref)

			wantObservation(t, ref, got, err, tc.want, nil, tc.detail)
		})
	}
}
