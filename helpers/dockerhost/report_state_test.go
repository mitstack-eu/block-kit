package dockerhost_test

// A host report carrying a state member is judged from that member alone, quotes each container's and release's output from beside it, and fails naming the member when the state is not an object.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/observe"
)

// stateReport carries the host's judged members in its state member, beside a top-level host that contradicts each of them and holds each container's output at the place the state lists that container.
const stateReport = `{"state":{"host":{` +
	`"capacity":{"allocatable":{"cpuMillis":3500,"memoryBytes":7516192768,"diskBytes":75161927680}},"agent":{"syncedCommit":"` + synced + `"},` +
	`"apps":{"shop":[{"container":"shop-web","subscription":"sub_shop","image":"ghcr.io/acme/shop@sha256:9f00","state":"exited"},` +
	`{"container":"shop-mariadb","subscription":"sub_shopdb","image":"mariadb@sha256:db00","state":"running","health":"unhealthy"}]},` +
	`"releases":{"shop":{"subscription":"sub_shop","container":"app-shop-release","image":"ghcr.io/acme/shop@sha256:ab12","exitCode":1}},` +
	`"volumes":[{"app":"shop","block":"docker-app-slice"}],"backups":{"runs":[],"error":null}}},` +
	`"host":{"capacity":{"error":"the host could not read its disk"},"agent":{"syncedCommit":"0000000"},` +
	`"apps":{"shop":[{"subscription":"sub_other","state":"running","health":"healthy","logs":"PHP Fatal error: Uncaught PDOException\n"},` +
	`{"subscription":"sub_other","state":"running","health":"healthy","logs":"mariadbd: ready for connections.\n","healthCheck":{"output":"mysqladmin: connect to server failed"}}]},` +
	`"releases":{"shop":{"subscription":"sub_other","image":"ghcr.io/acme/shop@sha256:0000","exitCode":0,"output":"SQLSTATE[42S01]: Table 'orders' already exists\n"}},` +
	`"volumes":[],"backups":{"runs":[],"error":"the host holds no backup public key"}}}`

func TestReadReport_TakesJudgedMembersFromState(t *testing.T) {
	report, err := dockerhost.ReadReport([]byte(stateReport))
	if err != nil {
		t.Fatalf("ReadReport = %v; want the report read", err)
	}
	web, db := "PHP Fatal error: Uncaught PDOException\n", "mariadbd: ready for connections.\n"
	want, _ := json.Marshal([]dockerhost.Container{
		{Name: "shop-web", Subscription: "sub_shop", Image: "ghcr.io/acme/shop@sha256:9f00", State: "exited", Logs: &web},
		{Name: "shop-mariadb", Subscription: "sub_shopdb", Image: "mariadb@sha256:db00", State: "running", Health: "unhealthy", Logs: &db,
			HealthCheck: dockerhost.HealthCheck{Output: "mysqladmin: connect to server failed"}},
	})
	wantLines := []string{
		"app-shop-release | the release command exited 1 on ghcr.io/acme/shop@sha256:ab12",
		"app-shop-release | SQLSTATE[42S01]: Table 'orders' already exists",
		"shop-web | PHP Fatal error: Uncaught PDOException",
	}

	containers, err := report.AppContainers("shop")
	lines := subscriptionLogs(t, "sub_shop", received(39, stateReport))

	if got, _ := json.Marshal(containers); err != nil || string(got) != string(want) {
		t.Errorf("AppContainers(shop) = %s, %v; want the state's containers, each with the output beside it, %s", got, err, want)
	}
	if !slices.Equal(lines, wantLines) {
		t.Errorf("SubscriptionLogs(sub_shop) = %q; want the state's release and container led by the output beside them, %q", lines, wantLines)
	}
}

func TestReportState_EveryReaderIgnoresTopLevelJudgedMembers(t *testing.T) {
	report := newest(t, received(39, stateReport))
	var decoded struct {
		Host struct{ Volumes []struct{ App string } }
	}

	allocatable, given := report.Allocatable()
	capacity, capacityErr := dockerhost.Capacity(report, true, "shop")
	commit, commitErr := report.SyncedCommit()
	release, releaseErr := report.AppRelease("shop", "sub_shop")
	asked, askedErr := report.ReleaseAgainst("shop", "sub_shop", "ghcr.io/acme/shop@sha256:ab12")
	unhealthy, waits, healthErr := report.AppHealth("shop", "sub_shopdb", appCauses)
	decodeErr := report.Decode(&decoded)
	done, doneErr := finalBackup("", new(string)).Done(context.Background(), &hostReports{reports: []observe.NodeReport{received(39, stateReport)}}, contract.Desired{}, hostSub)

	for _, tc := range []struct{ reader, got, want string }{
		{"Allocatable", fmt.Sprintf("%+v %v", allocatable, given), "{CPUMillis:3500 MemoryBytes:7516192768 DiskBytes:75161927680} true"},
		{"CapacityError", report.CapacityError(), ""},
		{"Capacity", fmt.Sprintf("%+v %v", capacity, capacityErr), "{CPUMillis:3500 MemoryBytes:7516192768 DiskBytes:75161927680} <nil>"},
		{"SyncedCommit", fmt.Sprintf("%s %v", commit, commitErr), synced + " <nil>"},
		{"AppRelease", fmt.Sprintf("%+v %v", release, releaseErr), "{Failed:true FailedDigest:sha256:ab12 ServingDigest:} <nil>"},
		{"ReleaseAgainst", fmt.Sprintf("%v %d %v", asked.FailedOnAsked(), asked.ExitCode, askedErr), "true 1 <nil>"},
		{"AppHealth", fmt.Sprintf("%+v %q %v", unhealthy, waits, healthErr),
			`&{Cause:the app's container reads unhealthy on the host Detail:shop-mariadb reads unhealthy, and its health check said "mysqladmin: connect to server failed"} "" <nil>`},
		{"Decode", fmt.Sprintf("%+v %v", decoded.Host.Volumes, decodeErr), "[{App:shop}] <nil>"},
		{"FinalBackup.Done", fmt.Sprintf("%v %v", done, doneErr),
			"false asked the host for the final backup final-1793620800 of shop's persisted files, which is kept until the host uploads it"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s over a report whose top-level host contradicts its state = %s; want the state's answer, %s", tc.reader, tc.got, tc.want)
		}
	}
}

// besideState moves each report the host sends into its state member, beside a top-level host that contradicts every judged member and holds the database container's output.
type besideState struct{ observe.NodeReportsAPI }

func (b besideState) NodeReports(ctx context.Context, d contract.Desired, sub string) ([]observe.NodeReport, error) {
	reports, err := b.NodeReportsAPI.NodeReports(ctx, d, sub)
	for i, r := range reports {
		reports[i].Body = []byte(`{"state":` + string(r.Body) + `,"host":{"capacity":{"allocatable":{"cpuMillis":1,"memoryBytes":1,"diskBytes":1}},` +
			`"agent":{"syncedCommit":"0000000"},"apps":{"shop":[{"subscription":"sub_other","state":"running","health":"healthy",` +
			`"logs":"FATAL: the store lost its data directory\n"}]},"volumes":[],"backups":{"runs":[],"error":"the host holds no backup public key"}}}`)
	}
	return reports, err
}

func TestReportState_EveryDatabaseReaderIgnoresTopLevelJudgedMembers(t *testing.T) {
	ctx := context.Background()
	e := newDBEnv(t)
	e.DB.Reports = besideState{e.Host}
	_, landErr := e.DB.EnsureResources(ctx, e.Desired("shop", nil))
	e.Containers["shop"] = map[string]any{"subscription": "sub_db_shop", "state": "exited"}
	e.Volumes["shop"] = true
	e.BackupResult = ""
	neverActive := e.Desired("shop", nil)
	neverActive.NeverActive = true

	obs, observeErr := e.DB.ObserveResource(ctx, contract.ResourceRef{Name: dockerhost.DatabaseResource, Desired: neverActive})
	lines, logsErr := e.DB.SubscriptionLogs(ctx, e.Desired("shop", nil))
	_, deleteErr := e.DB.Delete(ctx, e.Desired("shop", nil))

	if landErr != nil {
		t.Errorf("EnsureResources = %v; want the database landed on the state's room and backups", landErr)
	}
	if says := "shop-store reads exited: FATAL: the store lost its data directory"; observeErr != nil || !strings.Contains(obs.StatusDetail, says) {
		t.Errorf("ObserveResource = %+v, %v; want the state's exited container quoting the line beside it, %q", obs, observeErr, says)
	}
	if want := []string{"shop-store | FATAL: the store lost its data directory"}; logsErr != nil || !slices.Equal(lines, want) {
		t.Errorf("SubscriptionLogs = %q, %v; want %q", lines, logsErr, want)
	}
	if request := strings.TrimSpace(e.Main()["shop/"+dbBlock+".backup-request"]); request != "final-1793620800" {
		t.Errorf("Delete = %v asking for %q; want the final backup the state's volume and backups ask for, final-1793620800", deleteErr, request)
	}
}

func TestReadReport_RefusesAStateThatIsNotAnObject(t *testing.T) {
	for _, state := range []string{`[]`, `null`, `"healthy"`, `7`, `true`} {
		body := `{"state":` + state + `,"host":{"agent":{"syncedCommit":"` + synced + `"}}}`

		_, err := dockerhost.ReadReport([]byte(body))

		if err == nil || !strings.Contains(err.Error(), "state member") {
			t.Errorf("ReadReport(%s) = %v; want an error naming the state member, never a read of the top-level host", body, err)
		}
	}
}
