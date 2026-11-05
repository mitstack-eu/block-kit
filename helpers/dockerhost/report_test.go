package dockerhost_test

// A Docker host's newest node report answers its allocatable capacity, the commit its agent last synced from host.agent.syncedCommit, never from the agent's log, whether an app's newest release command failed and what serves it, and one subscription's own containers' output, with an explicit reason when it cannot.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/observe"
)

const (
	hostSub = "sub_host"
	synced  = "6e40c8f03e5b2a7c9d1f4e8a0b3c6d9e2f5a8b1c"
)

type hostReports struct {
	reports []observe.NodeReport
	err     error
}

func (h *hostReports) NodeReports(_ context.Context, _ contract.Desired, sub string) ([]observe.NodeReport, error) {
	if sub != hostSub {
		return nil, errors.New("asked for another subscription's reports")
	}
	return h.reports, h.err
}

func received(minute int, body string) observe.NodeReport {
	return observe.NodeReport{ReceivedAt: time.Date(2026, 11, 2, 17, minute, 0, 0, time.UTC), Body: []byte(body)}
}

func newest(t *testing.T, reports ...observe.NodeReport) dockerhost.Report {
	t.Helper()
	r, found, err := dockerhost.NewestReport(context.Background(), &hostReports{reports: reports}, contract.Desired{}, hostSub)
	if err != nil || !found {
		t.Fatalf("NewestReport = found %v, %v; want the newest report", found, err)
	}
	return r
}

func TestNewestReport_AnswersTheCommitAndCapacityOfTheNewestReport(t *testing.T) {
	r := newest(t,
		received(39, `{"host":{"capacity":{"allocatable":{"cpuMillis":3500,"memoryBytes":7516192768}},"agent":{"output":"Pulling fs layer\n","syncedCommit":"`+synced+`"}}}`),
		received(38, `{"host":{"agent":{"output":"","syncedCommit":"0123456789abcdef0123456789abcdef01234567"}}}`),
	)

	commit, err := r.SyncedCommit()

	if err != nil || commit != synced {
		t.Errorf("SyncedCommit = %q, %v; want the newest report's %s", commit, err, synced)
	}
	if r.Allocatable == nil || *r.Allocatable != (dockerhost.Reservation{CPUMillis: 3500, MemoryBytes: 7516192768}) {
		t.Errorf("Allocatable = %+v, want the newest report's", r.Allocatable)
	}
	var body struct {
		Host struct{ Agent struct{ Output string } }
	}
	if err := r.Decode(&body); err != nil || body.Host.Agent.Output != "Pulling fs layer\n" {
		t.Errorf("Decode = %+v, %v; want the newest report's own fields", body, err)
	}
}

func TestNewestReport_AnswersNoCommitWhenTheReportCannotNameOne(t *testing.T) {
	for _, tc := range []struct{ name, agent, want string }{
		{"a host on a release without the field, whatever its log says", `{"output":"agent: synced ` + synced + `\n"}`,
			"no host.agent.syncedCommit, so the host runs a hcloud-docker-host release before 0.8.0; upgrade the host to 0.8.0"},
		{"an agent that has not synced on this release", `{"output":"","syncedCommit":null}`, "has not synced since it took its current release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newest(t, received(39, `{"host":{"agent":`+tc.agent+`}}`))

			commit, err := r.SyncedCommit()

			if commit != "" || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("SyncedCommit = %q, %v; want no commit and a reason naming %q", commit, err, tc.want)
			}
		})
	}
}

func TestNewestReport_FindsNoneWhileTheHostHasSentNoReport(t *testing.T) {
	_, found, err := dockerhost.NewestReport(context.Background(), &hostReports{}, contract.Desired{}, hostSub)

	if found || err != nil {
		t.Errorf("NewestReport = found %v, %v; want none found and no error", found, err)
	}
}

func TestNewestReport_FailsAReadItCannotMake(t *testing.T) {
	for _, tc := range []struct {
		name, sub, class string
		api              *hostReports
	}{
		{"a host binding with no subscription", "", "host-binding", &hostReports{}},
		{"a platform that does not answer", hostSub, "", &hostReports{err: errors.New("the platform answered 503")}},
		{"a report that is not JSON", hostSub, "", &hostReports{reports: []observe.NodeReport{received(39, `{"host":`)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := dockerhost.NewestReport(context.Background(), tc.api, contract.Desired{}, tc.sub)

			var refused *handler.Refused
			if err == nil || errors.As(err, &refused) != (tc.class != "") || (refused != nil && refused.Class != tc.class) {
				t.Errorf("NewestReport error = %v, want an error refused as %q", err, tc.class)
			}
		})
	}
}

// twoApps is a report of a host running shop, with its database and a container no block labelled, and blog, each block's container labelled with its subscription.
const twoApps = `{"host":{"apps":{` +
	`"shop":[{"container":"shop-web","subscription":"sub_shop","logs":"PHP Fatal error: Uncaught PDOException\nGET / 500\n"},` +
	`{"container":"shop-mariadb","subscription":"sub_shopdb","logs":"mariadbd: ready for connections.\n"},` +
	`{"container":"shop-worker","subscription":null,"logs":"worker: polling the queue\n"}],` +
	`"blog":[{"container":"blog-web","subscription":"sub_blog","logs":"blog: listening on :8080\n"}]},` +
	`"releases":{"shop":{"subscription":"sub_shop","container":"app-shop-release","image":"ghcr.io/acme/shop@sha256:ab12","exitCode":1,` +
	`"output":"Migrating: 2026_11_05_create_orders\nSQLSTATE[42S01]: Table 'orders' already exists\n"},` +
	`"blog":{"subscription":"sub_blog","container":"app-blog-release","image":"ghcr.io/acme/blog@sha256:cd34","exitCode":0,"output":"blog migrated\n"}}}}`

func subscriptionLogs(t *testing.T, sub string, reports ...observe.NodeReport) []string {
	t.Helper()
	d := contract.Desired{Identity: contract.Identity{SubscriptionID: sub}}
	lines, err := dockerhost.SubscriptionLogs(context.Background(), &hostReports{reports: reports}, d, hostSub)
	if err != nil {
		t.Fatalf("SubscriptionLogs(%s) = %v, want its lines", sub, err)
	}
	return lines
}

func TestSubscriptionLogs_AnswersOnlyTheSubscriptionsOwnContainersFromTheNewestReport(t *testing.T) {
	for _, tc := range []struct {
		name, sub string
		want      []string
	}{
		{"an app whose release command failed reads that command's output first, then its container's lines", "sub_shop", []string{
			"app-shop-release | the release command exited 1 on ghcr.io/acme/shop@sha256:ab12",
			"app-shop-release | Migrating: 2026_11_05_create_orders",
			"app-shop-release | SQLSTATE[42S01]: Table 'orders' already exists",
			"shop-web | PHP Fatal error: Uncaught PDOException",
			"shop-web | GET / 500",
		}},
		{"the app's database on the same host reads its own container alone", "sub_shopdb", []string{
			"shop-mariadb | mariadbd: ready for connections.",
		}},
		{"another app on the same host reads none of shop's lines", "sub_blog", []string{
			"app-blog-release | the release command exited 0 on ghcr.io/acme/blog@sha256:cd34",
			"app-blog-release | blog migrated",
			"blog-web | blog: listening on :8080",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := subscriptionLogs(t, tc.sub, received(39, twoApps), received(38, `{"host":{"apps":{"shop":[{"container":"shop-web","subscription":"sub_shop","logs":"an older report's line\n"}]}}}`))

			if strings.Join(lines, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("SubscriptionLogs(%s) = %q; want %q", tc.sub, lines, tc.want)
			}
		})
	}
}

func TestSubscriptionLogs_SaysWhyItHasNoLinesRatherThanAnsweringNone(t *testing.T) {
	for _, tc := range []struct {
		name, sub string
		reports   []observe.NodeReport
		want      []string
	}{
		{"a host whose server was replaced and has sent no report yet", "sub_shop", nil,
			[]string{"The host has sent no node report yet, so this part's output shows once its first report arrives."}},
		{"a host on a release whose report carries no container output", "sub_shop",
			[]observe.NodeReport{received(39, `{"host":{"apps":{"shop":[{"container":"shop-web","state":"running"}]}}}`)},
			[]string{"The host's newest node report carries no container's output, so its hcloud-docker-host release predates it; upgrade the host to the release whose node report carries each app container's output and its last release command's output."}},
		{"a subscription none of whose containers the host runs yet", "sub_new", []observe.NodeReport{received(39, twoApps)},
			[]string{"The host's newest node report names no container of this part, so it has not started on the host yet."}},
		{"a container that has written nothing", "sub_shop",
			[]observe.NodeReport{received(39, `{"host":{"apps":{"shop":[{"container":"shop-web","subscription":"sub_shop","logs":""}]}}}`)},
			[]string{"shop-web | wrote no output yet"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := subscriptionLogs(t, tc.sub, tc.reports...)

			if strings.Join(lines, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("SubscriptionLogs(%s) = %q; want %q", tc.sub, lines, tc.want)
			}
		})
	}
}

func TestSubscriptionLogs_FailsAReadItCannotMake(t *testing.T) {
	d := contract.Desired{Identity: contract.Identity{SubscriptionID: "sub_shop"}}

	lines, err := dockerhost.SubscriptionLogs(context.Background(), &hostReports{err: errors.New("the platform answered 503")}, d, hostSub)

	if lines != nil || err == nil || !strings.Contains(err.Error(), "the platform answered 503") {
		t.Errorf("SubscriptionLogs = %q, %v; want no lines and the platform's error", lines, err)
	}
}

// releases is a report of a host where shop rolled back from a failed release, fresh's first release failed, and blog's passed.
const releases = `{"host":{"apps":{` +
	`"shop":[{"container":"shop-web","subscription":"sub_shop","image":"ghcr.io/acme/shop@sha256:9f00","state":"running"},` +
	`{"container":"shop-mariadb","subscription":"sub_shopdb","image":"mariadb@sha256:db00","state":"running"}],` +
	`"fresh":[{"container":"fresh-mariadb","subscription":"sub_freshdb","image":"mariadb@sha256:db00","state":"running"},` +
	`{"container":"fresh-web","subscription":"sub_fresh","image":"ghcr.io/acme/fresh@sha256:ee00","state":"exited"}],` +
	`"blog":[{"container":"blog-web","subscription":"sub_blog","image":"ghcr.io/acme/blog@sha256:cd34","state":"running"}]},` +
	`"releases":{"shop":{"subscription":"sub_shop","image":"ghcr.io/acme/shop@sha256:ab12","exitCode":1},` +
	`"fresh":{"subscription":"sub_fresh","image":"ghcr.io/acme/fresh@sha256:ee00","exitCode":137},` +
	`"blog":{"subscription":"sub_blog","image":"ghcr.io/acme/blog@sha256:cd34","exitCode":0}}}}`

func TestReport_AppReleaseReadsWhetherTheNewestReleaseCommandFailedAndWhatServes(t *testing.T) {
	for _, tc := range []struct {
		name, app, sub string
		want           dockerhost.AppRelease
		serves         bool
	}{
		{"an app rolled back from a failed release reads the failed digest and the previous one serving", "shop", "sub_shop",
			dockerhost.AppRelease{Failed: true, FailedDigest: "sha256:ab12", ServingDigest: "sha256:9f00"}, true},
		{"a failed first deploy reads no digest serving, its database's and a stopped container's aside", "fresh", "sub_fresh",
			dockerhost.AppRelease{Failed: true, FailedDigest: "sha256:ee00"}, false},
		{"an app whose newest release passed reads not failed", "blog", "sub_blog", dockerhost.AppRelease{ServingDigest: "sha256:cd34"}, true},
		{"an app with no release command, so no release in the report, reads not failed", "new", "sub_new", dockerhost.AppRelease{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newest(t, received(39, releases)).AppRelease(tc.app, tc.sub)

			if err != nil || got != tc.want || got.Serves() != tc.serves {
				t.Errorf("AppRelease(%s, %s) = %+v serving %v, %v; want %+v serving %v", tc.app, tc.sub, got, got.Serves(), err, tc.want, tc.serves)
			}
		})
	}
}

func TestReport_AppReleaseFailsAReleaseItCannotRead(t *testing.T) {
	_, err := newest(t, received(39, `{"host":{"releases":{"shop":{"exitCode":"one"}}}}`)).AppRelease("shop", "sub_shop")

	if err == nil || !strings.Contains(err.Error(), "release") {
		t.Errorf("AppRelease = %v; want an error saying the release could not be read", err)
	}
}
