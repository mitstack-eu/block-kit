package gitops_test

// Check reads Argo CD's notification of the block's own commit, or of a later revision in which no commit changed its files, and judges the commit by its objects in the newest node report made since the verb; Wait checks until the verdict is no longer pending.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/gitops"
	"github.com/mitstack-eu/block-kit/capabilities/github/gitops/gitopstest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/events"
	"github.com/mitstack-eu/block-kit/observe"
)

// adrBody is the notification body ADR 0072 quotes, verbatim.
const adrBody = `{"app":"app-alpha","project":"app-alpha","sync":"Synced","revision":"9ee9b7e28c000b16a89b7b717e5f3b9fa6fcf611","health":"Healthy","phase":"Succeeded","message":"successfully synced (all tasks run)","images":null,"reconciledAt":"2026-09-28T10:47:23Z"}`

const adrRevision = "9ee9b7e28c000b16a89b7b717e5f3b9fa6fcf611"

const platformToken = "evt-tok-0123456789abcdef"

const alphaCluster = "sub-cluster"

var (
	alphaNamespace = gitops.Object{Kind: "Namespace", Name: "app-alpha"}
	alphaAccount   = gitops.Object{Kind: "ServiceAccount", Namespace: "app-alpha", Name: "app-alpha-runtime"}
)

func decodeADR(t *testing.T, body string) gitops.Notification {
	t.Helper()
	var n gitops.Notification
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		t.Fatalf("decoding the notification body: %v", err)
	}
	return n
}

func note(phase, health string) gitops.Notification {
	return gitops.Notification{App: "app-alpha", Revision: adrRevision, Phase: phase, Health: health}
}

func deployOfAlpha(repo gitops.Repository, notes gitops.Notifications, reports observe.NodeReportsAPI) gitops.Deployment {
	return gitops.Deployment{Repository: repo, Notifications: notes, NodeReports: reports, Application: "app-alpha", Cluster: alphaCluster, Paths: []string{nsPath, saPath}}
}

func alphaReport(reports *gitopstest.NodeReports, revision, health string) {
	reports.Report(alphaCluster, verbAt.Add(time.Minute), gitopstest.Application{Name: "app-alpha", Revision: revision, Objects: []gitopstest.Object{
		gitopstest.Healthy(alphaNamespace), {Object: alphaAccount, Sync: "Synced", Health: health, Message: "the account's token Secret is missing"},
	}})
}

func TestNotification_DecodesAndReencodesTheBodyADR0072Quotes(t *testing.T) {
	want := gitops.Notification{
		App: "app-alpha", Project: "app-alpha", Sync: "Synced", Revision: adrRevision, Health: "Healthy", Phase: "Succeeded",
		Message: "successfully synced (all tasks run)", ReconciledAt: "2026-09-28T10:47:23Z",
	}

	n := decodeADR(t, adrBody)
	raw, err := json.Marshal(n)

	if !reflect.DeepEqual(n, want) {
		t.Errorf("decoded %+v, want %+v", n, want)
	}
	var fields map[string]any
	if err != nil || json.Unmarshal(raw, &fields) != nil || fields["reconciledAt"] != "2026-09-28T10:47:23Z" {
		t.Errorf("re-encoded %s, %v; want reconciledAt carried through as 2026-09-28T10:47:23Z", raw, err)
	}
}

func TestCheck(t *testing.T) {
	boom := errors.New("notification store unreachable")
	cases := []struct {
		name      string
		seed      func(notes *gitopstest.Notifications, reports *gitopstest.NodeReports, revision string)
		want      gitops.Verdict
		wantNote  bool
		wantErr   error
		wantReads int
	}{
		{"it reads the notification at the revision once and returns it beside the verdict", func(notes *gitopstest.Notifications, reports *gitopstest.NodeReports, revision string) {
			notes.Set(decodeADR(t, strings.ReplaceAll(adrBody, adrRevision, revision)))
			alphaReport(reports, revision, "Healthy")
		}, gitops.VerdictSucceeded, true, nil, 1},
		{"no notification and no report yet is pending on the revision handed in", func(*gitopstest.Notifications, *gitopstest.NodeReports, string) {}, gitops.VerdictPending, false, nil, 1},
		{"a source error is returned", func(notes *gitopstest.Notifications, _ *gitopstest.NodeReports, _ string) { notes.FailWith(boom) }, gitops.VerdictPending, false, boom, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, notes, reports := gitopstest.NewRepository(), gitopstest.NewNotifications(), gitopstest.NewNodeReports()
			revision := mustCommit(t, repo, alphaFiles())
			tc.seed(notes, reports, revision)

			out, err := gitops.Check(context.Background(), deployOfAlpha(repo, notes, reports), revision)

			if out.Verdict != tc.want || out.Revision != revision || (out.Notification.Revision == revision) != tc.wantNote || !errors.Is(err, tc.wantErr) {
				t.Errorf("Check = %+v, %v; want %s on %s, its notification kept %v, error %v", out, err, tc.want, revision, tc.wantNote, tc.wantErr)
			}
			if got := notes.Reads("app-alpha", revision); tc.wantReads > 0 && got != tc.wantReads {
				t.Errorf("Check read %d times, want %d", got, tc.wantReads)
			}
		})
	}
}

// unfolding answers each read with the reports of the next state in turn, then repeats the last.
type unfolding struct {
	states []*gitopstest.NodeReports
	reads  int
}

func (u *unfolding) NodeReports(ctx context.Context, d contract.Desired, cluster string) ([]observe.NodeReport, error) {
	i := min(u.reads, len(u.states)-1)
	u.reads++
	return u.states[i].NodeReports(ctx, d, cluster)
}

func TestWait(t *testing.T) {
	cases := []struct {
		name     string
		healths  []string
		timeout  time.Duration
		want     gitops.Verdict
		wantErr  error
		minReads int
		exact    bool
	}{
		{"it polls through no report and a Progressing object until succeeded, and reads no more", []string{"", "Progressing", "Healthy"},
			5 * time.Second, gitops.VerdictSucceeded, nil, 3, true},
		{"a failed verdict ends the wait as an outcome, not an error", []string{"Progressing", "Degraded"},
			5 * time.Second, gitops.VerdictFailed, nil, 2, true},
		{"a context ending while pending returns its error with a pending outcome after polling", []string{"Progressing"},
			30 * time.Millisecond, gitops.VerdictPending, context.DeadlineExceeded, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, notes := gitopstest.NewRepository(), gitopstest.NewNotifications()
			revision := mustCommit(t, repo, alphaFiles())
			notes.Set(gitops.Notification{App: "app-alpha", Revision: revision, Phase: "Succeeded", Health: "Progressing"})
			reports := &unfolding{}
			for _, health := range tc.healths {
				state := gitopstest.NewNodeReports()
				if health != "" {
					alphaReport(state, revision, health)
				}
				reports.states = append(reports.states, state)
			}
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()

			out, err := gitops.Wait(ctx, deployOfAlpha(repo, notes, reports), revision, time.Millisecond)

			if out.Verdict != tc.want || out.Revision != revision || out.Detail == "" || !errors.Is(err, tc.wantErr) {
				t.Errorf("Wait = %+v, %v; want %s on %s with a detail, error %v", out, err, tc.want, revision, tc.wantErr)
			}
			if reads := reports.reads; reads < tc.minReads || (tc.exact && reads != tc.minReads) {
				t.Errorf("Wait read the reports %d times, want %d (exactly: %v)", reads, tc.minReads, tc.exact)
			}
		})
	}
}

func TestWait_RefusesAPollIntervalThatIsNotPositive(t *testing.T) {
	_, err := gitops.Wait(context.Background(), deployOfAlpha(gitopstest.NewRepository(), gitopstest.NewNotifications(), gitopstest.NewNodeReports()), adrRevision, 0)

	if err == nil {
		t.Error("Wait with poll 0 = nil error, want a refusal")
	}
}

func TestCheck_SharedRepository(t *testing.T) {
	alpha, beta := alphaFiles(), betaFiles()
	beta2 := map[string][]byte{betaPath: []byte("beta 2\n")}
	labelled := []byte("apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: app-alpha-runtime\n  namespace: app-alpha\n  labels: {tier: web}\n")
	healthy, degraded := note("Succeeded", "Healthy"), note("Succeeded", "Degraded")
	cases := []struct {
		name         string
		history      []map[string][]byte
		recorded     int
		notified     int
		n            gitops.Notification
		reported     int
		account      string
		want         gitops.Verdict
		wantOwn      int
		wantNotified int
		wantDetail   []string
	}{
		{"a sibling's later commit Argo CD reports, holding the block's files, settles the block on it",
			[]map[string][]byte{alpha, beta}, 0, 1, healthy, 1, "Healthy", gitops.VerdictSucceeded, 0, 1, nil},
		{"a sibling's later commit Argo CD reports degraded fails the block, since success still needs the health it needed",
			[]map[string][]byte{alpha, beta}, 0, 1, degraded, 1, "Degraded", gitops.VerdictFailed, 0, 1, []string{"ServiceAccount app-alpha/app-alpha-runtime reads Degraded"}},
		{"a later revision that changes the block's file is not settled on, naming the commit and the file",
			[]map[string][]byte{alpha, {saPath: labelled}}, 0, 1, healthy, -1, "", gitops.VerdictPending, 0, -1, []string{"@1", saPath}},
		{"a later revision that drops the block's file is not settled on, naming the commit and the file",
			[]map[string][]byte{alpha, {nsPath: nil}}, 0, 1, healthy, -1, "", gitops.VerdictPending, 0, -1, []string{"@1", nsPath}},
		{"Argo CD reporting only a sibling's commit from before the block's own leaves the block pending on its own",
			[]map[string][]byte{beta, alpha}, 1, 0, healthy, -1, "", gitops.VerdictPending, 1, -1, nil},
		{"a recorded sibling's commit is traced back to the block's own, and a newer revision holding its files settles it",
			[]map[string][]byte{alpha, beta, beta2}, 1, 2, healthy, 2, "Healthy", gitops.VerdictSucceeded, 0, 2, nil},
		{"a recorded sibling's commit is traced back to the block's own, whose own notification settles it",
			[]map[string][]byte{alpha, beta}, 1, 0, healthy, 1, "Healthy", gitops.VerdictSucceeded, 0, 0, nil},
		{"a notification at exactly the recorded revision is judged on it as before",
			[]map[string][]byte{alpha, beta}, 1, 1, healthy, 1, "Healthy", gitops.VerdictSucceeded, 1, 1, nil},
		{"no notification at all leaves the block pending on its own commit",
			[]map[string][]byte{alpha, beta}, 0, -1, healthy, -1, "", gitops.VerdictPending, 0, -1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := gitopstest.NewRepository()
			var revisions []string
			for i, files := range tc.history {
				head, _ := repo.Head(context.Background())
				rev, err := repo.Commit(context.Background(), head, files, "commit "+strconv.Itoa(i))
				if err != nil {
					t.Fatal(err)
				}
				revisions = append(revisions, rev)
			}
			notes, reports := gitopstest.NewNotifications(), gitopstest.NewNodeReports()
			if tc.notified >= 0 {
				n := tc.n
				n.Revision = revisions[tc.notified]
				notes.Set(n)
			}
			if tc.reported >= 0 {
				alphaReport(reports, revisions[tc.reported], tc.account)
			}
			at := func(i int) string {
				if i < 0 {
					return ""
				}
				return revisions[i]
			}

			out, err := gitops.Check(context.Background(), deployOfAlpha(repo, notes, reports), revisions[tc.recorded])

			if err != nil || out.Verdict != tc.want || out.Revision != at(tc.wantOwn) || out.Notification.Revision != at(tc.wantNotified) {
				t.Fatalf("Check(%s) = %+v, %v over %v; want %q on own %q judged at %q", at(tc.recorded), out, err, revisions, tc.want, at(tc.wantOwn), at(tc.wantNotified))
			}
			for _, want := range tc.wantDetail {
				if want == "@1" {
					want = revisions[1]
				}
				if !strings.Contains(out.Detail, want) {
					t.Errorf("Detail = %q, want it to name %q", out.Detail, want)
				}
			}
			if out.Detail == "" {
				t.Error("Detail is empty, want it to say what the verdict rests on")
			}
		})
	}
}

type seenRequest struct{ method, path, auth, app, revision string }

func TestPlatformNotifications_Latest(t *testing.T) {
	cases := []struct {
		name      string
		revision  string
		status    int
		body      string
		nilClient bool
		wantOK    bool
		wantReq   seenRequest
		wantInErr []string
	}{
		{"it gets the app's notification at the revision with the bearer token", adrRevision, 200, adrBody, false, true,
			seenRequest{http.MethodGet, "/v1/gitops-notifications", "Bearer " + platformToken, "app-alpha", adrRevision}, nil},
		{"revision \"\" asks for the app's newest with no revision parameter", "", 200, adrBody, false, true,
			seenRequest{http.MethodGet, "/v1/gitops-notifications", "Bearer " + platformToken, "app-alpha", "<none>"}, nil},
		{"a nil HTTPClient uses a default", adrRevision, 200, adrBody, true, true, seenRequest{}, nil},
		{"a 404 means no notification yet", adrRevision, 404, `{"error":"no notification"}`, false, false, seenRequest{}, nil},
		{"any other status is an error carrying status and body but never the token", adrRevision, 502, "upstream rejected ", false, false, seenRequest{},
			[]string{"502", "upstream rejected"}},
		{"an answer a byte past its bound fails naming the app and the bound", adrRevision, 200, `{"pad":"` + strings.Repeat("a", 64<<10+1-len(`{"pad":""}`)) + `"}`, false, false, seenRequest{},
			[]string{"app-alpha", "the answer is longer than 65536 bytes"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got seenRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = seenRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.URL.Query().Get("app"), r.URL.Query().Get("revision")}
				if !r.URL.Query().Has("revision") {
					got.revision = "<none>"
				}
				body := tc.body
				if tc.status == 502 {
					body += r.Header.Get("Authorization") // a misbehaving upstream echoes the credential it was handed
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			src := gitops.PlatformNotifications{BaseURL: srv.URL, Token: platformToken, HTTPClient: srv.Client()}
			if tc.nilClient {
				src.HTTPClient = nil
			}

			n, ok, err := src.Latest(context.Background(), "app-alpha", tc.revision)

			if ok != tc.wantOK || (err != nil) != (tc.wantInErr != nil) {
				t.Fatalf("Latest = %+v, %v, %v; want found %v, error %v", n, ok, err, tc.wantOK, tc.wantInErr != nil)
			}
			if tc.wantOK && !reflect.DeepEqual(n, decodeADR(t, adrBody)) {
				t.Errorf("Latest = %+v, want the ADR notification", n)
			}
			if tc.wantReq != (seenRequest{}) && got != tc.wantReq {
				t.Errorf("request = %+v, want %+v", got, tc.wantReq)
			}
			for _, want := range tc.wantInErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not carry %q", err, want)
				}
			}
			if err != nil && strings.Contains(err.Error(), platformToken) {
				t.Errorf("error %q carries the bearer token", err)
			}
		})
	}
}

func TestNotificationsFromContext(t *testing.T) {
	ctx := events.WithEnvelope(context.Background(), events.Envelope{SubscriptionID: "sub-1", Callback: &events.Callback{URL: "https://api.mitstack.dev/v1/webhooks/operator-events", Token: platformToken}})

	src, err := gitops.NotificationsFromContext(ctx)

	if err != nil || src.BaseURL != "https://api.mitstack.dev" || src.Token != platformToken {
		t.Errorf("NotificationsFromContext = base %q, token set %v, %v; want the platform the callback names and its token", src.BaseURL, src.Token == platformToken, err)
	}
	if _, err := gitops.NotificationsFromContext(context.Background()); err == nil {
		t.Error("NotificationsFromContext with no events callback = nil error, want the error events.PlatformFrom returns")
	}
}
