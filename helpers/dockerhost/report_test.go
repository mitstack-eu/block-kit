package dockerhost_test

// A Docker host's newest node report answers its allocatable capacity, the commit its agent last synced from host.agent.syncedCommit, never from the agent's log, whether an app's newest release command failed on the image its newest deploy asked for and what serves it, and one subscription's own containers' output, with an explicit reason when it cannot.

import (
	"context"
	"errors"
	"fmt"
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
		received(39, `{"host":{"capacity":{"allocatable":{"cpuMillis":3500,"memoryBytes":7516192768,"diskBytes":75161927680}},"agent":{"output":"Pulling fs layer\n","syncedCommit":"`+synced+`"}}}`),
		received(38, `{"host":{"agent":{"output":"","syncedCommit":"0123456789abcdef0123456789abcdef01234567"}}}`),
	)

	commit, err := r.SyncedCommit()
	capacity, capacityErr := dockerhost.Capacity(r, true, "shop")

	if err != nil || commit != synced {
		t.Errorf("SyncedCommit = %q, %v; want the newest report's %s", commit, err, synced)
	}
	if capacityErr != nil || capacity.CPUMillis != 3500 || capacity.MemoryBytes != 7516192768 || capacity.DiskBytes != 75161927680 {
		t.Errorf("Capacity = %+v, %v; want the newest report's 3500 millicores, 7516192768 memory bytes and 75161927680 disk bytes", capacity, capacityErr)
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
		{"an agent the report gives as text", `"restarting"`, "host.agent holds a JSON string"},
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

func TestNewestReport_AnswersAnAttestedAbsentHostWithoutReadingItsReports(t *testing.T) {
	report, found, err := dockerhost.NewestReport(context.Background(), &hostReports{err: errors.New("the platform answered 503")}, attestedAbsent(hostSub), hostSub)
	if _, notSynced := report.SyncedCommit(); !found || err != nil || notSynced == nil {
		t.Errorf("NewestReport = %+v, found %v, %v; want the host's attested absence, which syncs no commit", report, found, err)
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

func TestSubscriptionLogs_AnswersTheLinesOfAReportWhoseOtherPartsCannotBeRead(t *testing.T) {
	for _, tc := range []struct{ name, part string }{
		{"a capacity whose cpuMillis is text", `"capacity":{"allocatable":{"cpuMillis":"[redacted]6","memoryBytes":8000000000,"diskBytes":80000000000}}`},
		{"a capacity that is text", `"capacity":"unread"`},
		{"an agent that is text", `"agent":"restarting"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"host":{` + tc.part + `,"apps":{"shop":[{"container":"shop-web","subscription":"sub_shop","logs":"listening on :8080\n"}]}}}`

			lines := subscriptionLogs(t, "sub_shop", received(39, body))

			if want := "shop-web | listening on :8080"; strings.Join(lines, "\n") != want {
				t.Errorf("SubscriptionLogs(sub_shop) of %s = %q; want %q", body, lines, want)
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

const (
	imageA = "ghcr.io/acme/shop@sha256:aaaa"
	imageB = "ghcr.io/acme/shop@sha256:bbbb"
	imageC = "ghcr.io/acme/shop@sha256:cccc"
)

// shopReport is the node report of a host whose shop container serves serving, or none when serving is empty, and whose newest release command ran on ran and exited exitCode.
func shopReport(serving, ran string, exitCode int) string {
	apps := `"shop":[]`
	if serving != "" {
		apps = `"shop":[{"container":"shop-web","subscription":"sub_shop","image":"` + serving + `","state":"running"}]`
	}
	return fmt.Sprintf(`{"host":{"apps":{%s},"releases":{"shop":{"subscription":"sub_shop","image":"%s","exitCode":%d}}}}`, apps, ran, exitCode)
}

func TestImageFile_ReadsTheImageTheNewestDeployAskedFor(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       dockerhost.ImageFile
		asked      string
	}{
		{"a file of one line asks for the image it names, as every file did before a rollback could mark one",
			"APP_IMAGE=" + imageA + "\n", dockerhost.ImageFile{Image: imageA}, imageA},
		{"a file a rollback wrote asks for the image it rolled back from, while the host returns to the image it names",
			"APP_IMAGE=" + imageA + "\nAPP_ROLLED_BACK_FROM=" + imageB + "\n", dockerhost.ImageFile{Image: imageA, RolledBackFrom: imageB}, imageB},
		{"a line the file does not define is skipped, as the host's agent skips it",
			"# pinned by mitstack\nAPP_IMAGE=" + imageC + "\n", dockerhost.ImageFile{Image: imageC}, imageC},
		{"an empty file asks for no image", "", dockerhost.ImageFile{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dockerhost.ParseImageFile(tc.text)

			if got != tc.want || got.Asked() != tc.asked {
				t.Errorf("ParseImageFile(%q) = %+v asking %q; want %+v asking %q", tc.text, got, got.Asked(), tc.want, tc.asked)
			}
		})
	}
}

func TestImageFile_WritesTheTextItReadsBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		file dockerhost.ImageFile
		want string
	}{
		{"a deploy writes one line, the form the host's agent and every earlier file hold",
			dockerhost.ImageFile{Image: imageA}, "APP_IMAGE=" + imageA + "\n"},
		{"a rollback writes the image it returns to, then the image it rolled back from",
			dockerhost.ImageFile{Image: imageA, RolledBackFrom: imageB}, "APP_IMAGE=" + imageA + "\nAPP_ROLLED_BACK_FROM=" + imageB + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.file.String()

			if text != tc.want || dockerhost.ParseImageFile(text) != tc.file {
				t.Errorf("%+v.String() = %q reading back %+v; want %q", tc.file, text, dockerhost.ParseImageFile(text), tc.want)
			}
		})
	}
}

func TestReport_ReleaseAgainstJudgesTheNewestReleaseByTheImageAskedFor(t *testing.T) {
	for _, tc := range []struct {
		name, report, imageFile string
		want                    dockerhost.AskedRelease
		failedOnAsked, absent   bool
	}{
		{"image A's release passed and A serves, so nothing reads against the app",
			shopReport(imageA, imageA, 0), "APP_IMAGE=" + imageA + "\n",
			dockerhost.AskedRelease{AppRelease: dockerhost.AppRelease{ServingDigest: "sha256:aaaa"}, AskedDigest: "sha256:aaaa"}, false, false},
		{"image B's release failed and mitstack rolled back to A, so the release failed on the asked B while A serves",
			shopReport(imageA, imageB, 1), "APP_IMAGE=" + imageA + "\nAPP_ROLLED_BACK_FROM=" + imageB + "\n",
			dockerhost.AskedRelease{AppRelease: dockerhost.AppRelease{Failed: true, FailedDigest: "sha256:bbbb", ServingDigest: "sha256:aaaa"}, AskedDigest: "sha256:bbbb", ExitCode: 1}, true, false},
		{"after that rollback a deploy asks for the serving A again with no host commit, so B's failed release reads as nothing",
			shopReport(imageA, imageB, 1), "APP_IMAGE=" + imageA + "\n",
			dockerhost.AskedRelease{AppRelease: dockerhost.AppRelease{Failed: true, FailedDigest: "sha256:bbbb", ServingDigest: "sha256:aaaa"}, AskedDigest: "sha256:aaaa", ExitCode: 1}, false, false},
		{"a failed first deploy fails on the asked image with no image serving",
			shopReport("", imageB, 137), "APP_IMAGE=" + imageB + "\n",
			dockerhost.AskedRelease{AppRelease: dockerhost.AppRelease{Failed: true, FailedDigest: "sha256:bbbb"}, AskedDigest: "sha256:bbbb", ExitCode: 137}, true, false},
		{"a release that failed on the asked C reads failed though the earlier A still serves",
			shopReport(imageA, imageC, 2), "APP_IMAGE=" + imageC + "\n",
			dockerhost.AskedRelease{AppRelease: dockerhost.AppRelease{Failed: true, FailedDigest: "sha256:cccc", ServingDigest: "sha256:aaaa"}, AskedDigest: "sha256:cccc", ExitCode: 2}, true, false},
		{"a release that failed on the older B while no container runs the asked C reads the asked image absent and no release failed on it",
			shopReport(imageA, imageB, 1), "APP_IMAGE=" + imageC + "\n",
			dockerhost.AskedRelease{AppRelease: dockerhost.AppRelease{Failed: true, FailedDigest: "sha256:bbbb", ServingDigest: "sha256:aaaa"}, AskedDigest: "sha256:cccc", ExitCode: 1}, false, true},
		{"a release that passed on the asked B while A still serves leaves the container's health to its own check",
			shopReport(imageA, imageB, 0), "APP_IMAGE=" + imageA + "\nAPP_ROLLED_BACK_FROM=" + imageB + "\n",
			dockerhost.AskedRelease{AppRelease: dockerhost.AppRelease{ServingDigest: "sha256:aaaa"}, AskedDigest: "sha256:bbbb"}, false, false},
		{"after B's rolled-back failure a new commit's image C passes its release and serves, so the app reads healthy on C",
			shopReport(imageC, imageC, 0), "APP_IMAGE=" + imageC + "\n",
			dockerhost.AskedRelease{AppRelease: dockerhost.AppRelease{ServingDigest: "sha256:cccc"}, AskedDigest: "sha256:cccc"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := dockerhost.ParseImageFile(tc.imageFile).Asked()

			got, err := newest(t, received(39, tc.report)).ReleaseAgainst("shop", "sub_shop", asked)

			if err != nil || got != tc.want || got.FailedOnAsked() != tc.failedOnAsked || got.AskedAbsent() != tc.absent {
				t.Errorf("ReleaseAgainst(shop, sub_shop, %s) = %+v failed on asked %v, asked absent %v, %v; want %+v, %v, %v",
					asked, got, got.FailedOnAsked(), got.AskedAbsent(), err, tc.want, tc.failedOnAsked, tc.absent)
			}
		})
	}
}

func TestReport_ReleaseAgainstNoAskedImageReadsEveryFailedReleaseAsFailed(t *testing.T) {
	got, err := newest(t, received(39, shopReport(imageA, imageB, 1))).ReleaseAgainst("shop", "sub_shop", "")

	if err != nil || !got.FailedOnAsked() || got.AskedAbsent() {
		t.Errorf("ReleaseAgainst(shop, sub_shop, \"\") = %+v failed on asked %v, asked absent %v, %v; want the failure on B read as failed",
			got, got.FailedOnAsked(), got.AskedAbsent(), err)
	}
}

func TestReadReport_ReadsABodyTheCallerAlreadyHolds(t *testing.T) {
	report, err := dockerhost.ReadReport([]byte(shopReport(imageA, imageB, 1)))
	if err != nil {
		t.Fatalf("ReadReport = %v; want the report read", err)
	}

	got, err := report.ReleaseAgainst("shop", "", imageB)

	if err != nil || !got.FailedOnAsked() || got.ExitCode != 1 {
		t.Errorf("ReleaseAgainst(shop, \"\", %s) on a read body = %+v, %v; want the release failed on B with exit code 1", imageB, got, err)
	}
}

func TestReadReport_FailsABodyThatIsNotAReport(t *testing.T) {
	_, err := dockerhost.ReadReport([]byte(`not json`))

	if err == nil || !strings.Contains(err.Error(), "node report") {
		t.Errorf("ReadReport(not json) = %v; want an error naming the node report", err)
	}
}

func TestReport_CapacityErrorIsTheReasonAReportGivesInPlaceOfItsAllocatableCapacity(t *testing.T) {
	const unread = "nproc, free -b or df -B1 / printed nothing the report can read"
	const allocatable = `"allocatable":{"cpuMillis":3500,"memoryBytes":7516192768,"diskBytes":75161927680}`
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "a report whose capacity is the error object gives that error", body: `{"host":{"capacity":{"error":"` + unread + `"}}}`, want: unread},
		{name: "a report with no capacity gives no error", body: `{"host":{}}`},
		{name: "a report giving its allocatable capacity gives no error", body: `{"host":{"capacity":{` + allocatable + `}}}`},
		{name: "a report giving its allocatable capacity beside an error gives no error", body: `{"host":{"capacity":{` + allocatable + `,"error":"` + unread + `"}}}`},
		{name: "a null capacity beside an error gives that error", body: `{"host":{"capacity":{"allocatable":null,"error":"` + unread + `"}}}`, want: unread},
		{name: "a capacity without its CPU names cpuMillis", body: `{"host":{"capacity":{"allocatable":{"memoryBytes":1,"diskBytes":1}}}}`, want: "host.capacity.allocatable lacks cpuMillis"},
		{name: "a capacity without its memory names memoryBytes", body: `{"host":{"capacity":{"allocatable":{"cpuMillis":4000,"diskBytes":1}}}}`, want: "host.capacity.allocatable lacks memoryBytes"},
		{name: "a capacity without its disk names diskBytes", body: `{"host":{"capacity":{"allocatable":{"cpuMillis":4000,"memoryBytes":1}}}}`, want: "host.capacity.allocatable lacks diskBytes"},
		{name: "a negative amount names its measure", body: `{"host":{"capacity":{"allocatable":{"cpuMillis":4000,"memoryBytes":-1,"diskBytes":1}}}}`, want: "host.capacity.allocatable gives a negative memoryBytes, -1"},
		{name: "an amount given as text names its measure", body: `{"host":{"capacity":{"allocatable":{"cpuMillis":"[redacted]6","memoryBytes":1,"diskBytes":1}}}}`,
			want: "host.capacity.allocatable.cpuMillis holds a JSON string, which no amount can be read from"},
		{name: "an amount past what an int64 holds names its measure", body: `{"host":{"capacity":{"allocatable":{"cpuMillis":4000,"memoryBytes":1,"diskBytes":9223372036854775808}}}}`,
			want: "host.capacity.allocatable.diskBytes holds a JSON number 9223372036854775808, which no amount can be read from"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := dockerhost.ReadReport([]byte(tc.body))
			if err != nil {
				t.Fatalf("ReadReport(%s) = %v; want the report read", tc.body, err)
			}

			got := report.CapacityError()

			if got != tc.want {
				t.Errorf("CapacityError() of %s = %q; want %q", tc.body, got, tc.want)
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
