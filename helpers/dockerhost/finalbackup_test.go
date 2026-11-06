package dockerhost_test

// An unsubscribe's final backup asks the host once, waits for the report to name that request uploaded, and removes nothing on any other answer.

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
	heldVolume  = `"volumes":[{"app":"shop","block":"docker-app-slice"}]`
	askedObject = "shop/20261105T120000Z.age"
)

func backupRun(block, request, result string) string {
	return `{"app":"shop","block":"` + block + `","object":"` + askedObject + `","request":"` + request + `","result":"` + result + `"}`
}

func TestFinalBackup_AnswersEachStateTheHostReports(t *testing.T) {
	for _, tc := range []struct {
		name, held, report string
		d                  contract.Desired
		done, waits        bool
		says               string
		asks               string
	}{
		{name: "a report showing the host never made the volume is done at once", report: `{"host":{"volumes":[]}}`, done: true},
		{name: "a volume with no request asks for one and waits", report: `{"host":{` + heldVolume + `}}`, waits: true, says: "final-1793620800", asks: "final-1793620800"},
		{name: "a held request the host has not run waits", held: "final-7", report: `{"host":{` + heldVolume + `}}`, waits: true, says: "final-7"},
		{name: "another block's upload of the request still waits", held: "final-7", report: `{"host":{` + heldVolume + `,"backups":{"runs":[` + backupRun("docker-mariadb", "final-7", "uploaded") + `]}}}`, waits: true, says: "final-7"},
		{name: "an upload answering another request still waits", held: "final-7", report: `{"host":{` + heldVolume + `,"backups":{"runs":[` + backupRun("docker-app-slice", "final-6", "uploaded") + `]}}}`, waits: true, says: "final-7"},
		{name: "a held request that is not a final one asks afresh", held: "manual-1", report: `{"host":{` + heldVolume + `}}`, waits: true, says: "final-1793620800", asks: "final-1793620800"},
		{name: "the named upload is done", held: "final-7", report: `{"host":{` + heldVolume + `,"backups":{"runs":[` + backupRun("docker-app-slice", "final-7", "uploaded") + `]}}}`, done: true},
		{name: "a failed run fails naming its result", held: "final-7", report: `{"host":{` + heldVolume + `,"backups":{"runs":[` + backupRun("docker-app-slice", "final-7", "failed: the store answered 403") + `]}}}`, says: "the store answered 403"},
		{name: "a host the platform attests absent is done at once, whatever its last report held", d: attestedAbsent(hostSub), report: `{"host":{` + heldVolume + `}}`, done: true},
		{name: "an attestation for another host still asks and waits", d: attestedAbsent("sub_other"), report: `{"host":{` + heldVolume + `}}`, waits: true, says: "final-1793620800", asks: "final-1793620800"},
		{name: "a host that takes no backups fails naming why", report: `{"host":{` + heldVolume + `,"backups":{"error":"the host holds no backup public key"}}}`, says: "the host holds no backup public key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := ""
			f := finalBackup(tc.held, &asked)

			done, err := f.Done(context.Background(), &hostReports{reports: []observe.NodeReport{received(1, tc.report)}}, tc.d, hostSub)

			var wait *handler.Waiting
			if done != tc.done || errors.As(err, &wait) != tc.waits || (err != nil) != (tc.says != "") {
				t.Errorf("Done over %s = %v, %v; want done %v, waits %v, says %q", tc.report, done, err, tc.done, tc.waits, tc.says)
			}
			if err != nil && !strings.Contains(err.Error(), tc.says) {
				t.Errorf("Done over %s = %v; want it naming %q", tc.report, err, tc.says)
			}
			if asked != tc.asks {
				t.Errorf("Done over %s asked for %q; want %q", tc.report, asked, tc.asks)
			}
		})
	}
}

func TestFinalBackup_WaitsWhileTheHostHasSentNoReport(t *testing.T) {
	asked := ""

	done, err := finalBackup("", &asked).Done(context.Background(), &hostReports{}, contract.Desired{}, hostSub)

	var wait *handler.Waiting
	if done || !errors.As(err, &wait) || asked != "" {
		t.Errorf("Done with no report = %v, %v, asking %q; want a wait and no request", done, err, asked)
	}
}

func attestedAbsent(sub string) contract.Desired {
	return contract.Desired{Bindings: map[string]map[string]string{"host": {"subscriptionId": sub}}, Absences: map[string]contract.Absence{"host": {Observer: "hetzner.docker-host", Resource: "hetzner.server", ReadAbsentAt: time.Unix(1793620000, 0), ProvesAbsence: true}}}
}

func finalBackup(held string, asked *string) dockerhost.FinalBackup {
	return dockerhost.FinalBackup{
		App: "shop", Block: "docker-app-slice", Kept: "shop's persisted files", Now: time.Unix(1793620800, 0),
		Held: func(context.Context) (string, error) { return held, nil },
		Ask: func(_ context.Context, request string) error {
			*asked = request
			return nil
		},
	}
}
